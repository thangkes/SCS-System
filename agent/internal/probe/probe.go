// Package probe: agent tự kiểm thiết bị trong LAN (ping ICMP / mở kết nối TCP) theo rule nguồn "probe"
// của bộ rule đã ký (schema 3), rồi đưa kết quả vào Engine như một dòng log để rule khớp / đếm / báo.
//
// Chỉ kiểm những gì rule đã ký yêu cầu, trong giới hạn của common/rules (IP nội bộ cố định hoặc {gateway},
// ≤ 16 đích, nhịp ≥ 60 giây); lúc chạy kiểm lại IP nội bộ (cả IP thật của {gateway}). Không cần thêm quyền:
// ICMP dùng socket ping không đặc quyền của Linux (net.ipv4.ping_group_range), TCP chỉ mở kết nối.
// Tắt hẳn trên máy bằng SCS_PROBES=off trong agent.env.
package probe

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thangkes/SCS-System/common/rules"
)

// ErrUnsupported: máy không kiểm được kiểu này (không phải Linux, hoặc không có quyền ping).
var ErrUnsupported = errors.New("không hỗ trợ trên máy này")

// Runner chạy các rule kiểm thiết bị cho tới khi ctx kết thúc.
type Runner struct {
	Log *slog.Logger
	Out chan<- rules.Line
	// Đổi được khi test.
	Ping    func(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, error)
	Dial    func(ctx context.Context, ip netip.Addr, port int, timeout time.Duration) (time.Duration, error)
	Gateway func() (netip.Addr, error)
	Now     func() time.Time
	// FirstDelay: chờ trước lần kiểm đầu (mặc định ngẫu nhiên trong một nhịp, để các máy không kiểm cùng lúc).
	FirstDelay func(every time.Duration) time.Duration

	mu   sync.Mutex
	down map[string]bool // khóa đích (rule + kiểu + đích + cổng) đang down
	warn map[string]time.Time
}

func (r *Runner) defaults() {
	if r.Ping == nil {
		r.Ping = ping
	}
	if r.Dial == nil {
		r.Dial = dialTCP
	}
	if r.Gateway == nil {
		r.Gateway = defaultGateway
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.FirstDelay == nil {
		r.FirstDelay = func(every time.Duration) time.Duration { return rand.N(every) }
	}
	r.down = map[string]bool{}
	r.warn = map[string]time.Time{}
}

// Run chạy mọi rule kiểm thiết bị; trả về khi ctx kết thúc.
func (r *Runner) Run(ctx context.Context, specs []rules.ProbeSpec) {
	r.defaults()
	var wg sync.WaitGroup
	for _, sp := range specs {
		wg.Add(1)
		go func() { defer wg.Done(); r.loop(ctx, sp) }()
	}
	wg.Wait()
}

func (r *Runner) loop(ctx context.Context, sp rules.ProbeSpec) {
	every := time.Duration(max(sp.Probe.Every(), rules.MinProbeEveryS)) * time.Second
	wait := r.FirstDelay(every)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		r.Round(ctx, sp)
		wait = every
	}
}

// Round kiểm lần lượt mọi đích của một rule một lượt (tuần tự, không bắn dồn).
func (r *Runner) Round(ctx context.Context, sp rules.ProbeSpec) {
	if r.down == nil {
		r.defaults()
	}
	p := sp.Probe
	timeout := time.Duration(min(max(p.Timeout(), 100), rules.MaxProbeTimeoutMs)) * time.Millisecond
	for _, t := range p.Targets {
		if ctx.Err() != nil {
			return
		}
		ip, label, err := r.resolve(t)
		if err != nil {
			r.warnOnce(sp.RuleID+" "+t, "không kiểm được đích", "rule", sp.RuleID, "dich", t, "err", err)
			continue
		}
		var rtt time.Duration
		switch p.Kind {
		case "icmp":
			rtt, err = r.Ping(ctx, ip, timeout)
		case "tcp":
			rtt, err = r.Dial(ctx, ip, p.Port, timeout)
		default:
			return
		}
		if errors.Is(err, ErrUnsupported) {
			r.warnOnce(sp.RuleID+" "+p.Kind, "máy không kiểm được kiểu này, bỏ qua", "rule", sp.RuleID, "kieu", p.Kind, "err", err)
			continue
		}
		key := sp.RuleID + " " + p.Key(label)
		target := label
		if p.Kind == "tcp" {
			target += ":" + strconv.Itoa(p.Port)
		}
		r.mu.Lock()
		wasDown := r.down[key]
		if err != nil {
			r.down[key] = true
		} else {
			delete(r.down, key)
		}
		r.mu.Unlock()
		switch {
		case err != nil:
			r.emit(ctx, sp.RuleID, fmt.Sprintf("probe %s %s down %s", p.Kind, target, reason(err)))
		case wasDown:
			r.emit(ctx, sp.RuleID, fmt.Sprintf("probe %s %s up rtt=%dms", p.Kind, target, rtt.Milliseconds()))
		}
	}
}

// resolve: IP thật của đích và nhãn ghi trong dòng log ({gateway} ghi IP thật). Luôn kiểm lại IP nội bộ.
func (r *Runner) resolve(t string) (netip.Addr, string, error) {
	var ip netip.Addr
	if t == rules.TargetGateway {
		gw, err := r.Gateway()
		if err != nil {
			return ip, "", fmt.Errorf("không đọc được router mặc định: %w", err)
		}
		ip = gw
	} else {
		var err error
		if ip, err = netip.ParseAddr(t); err != nil {
			return ip, "", err
		}
	}
	if !rules.ProbeAllowed(ip) {
		return ip, "", fmt.Errorf("%s không phải IP nội bộ: không kiểm", ip)
	}
	return ip, ip.String(), nil
}

func (r *Runner) emit(ctx context.Context, ruleID, text string) {
	select {
	case r.Out <- rules.Line{Source: rules.SourceProbe, Unit: ruleID, Time: r.Now(), Text: text}:
	case <-ctx.Done():
	}
}

// warnOnce: ghi log cảnh báo tối đa mỗi giờ một lần cho cùng một việc.
func (r *Runner) warnOnce(key, msg string, args ...any) {
	r.mu.Lock()
	last, ok := r.warn[key]
	now := r.Now()
	if ok && now.Sub(last) < time.Hour {
		r.mu.Unlock()
		return
	}
	r.warn[key] = now
	r.mu.Unlock()
	if r.Log != nil {
		r.Log.Warn(msg, args...)
	}
}

// reason: lý do ngắn gọn (một từ) để rule khớp được: timeout | refused | unreachable | error.
func reason(err error) string {
	var ne net.Error
	s := err.Error()
	switch {
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded), strings.Contains(s, "timeout"):
		return "timeout"
	case strings.Contains(s, "refused"):
		return "refused"
	case strings.Contains(s, "unreachable"), strings.Contains(s, "no route"):
		return "unreachable"
	}
	return "error"
}

// dialTCP: mở được kết nối TCP tới ip:port trong timeout thì thiết bị / dịch vụ còn sống.
func dialTCP(ctx context.Context, ip netip.Addr, port int, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp4", netip.AddrPortFrom(ip, uint16(port)).String())
	if err != nil {
		return 0, err
	}
	c.Close()
	return time.Since(start), nil
}

// parseRoute: dòng có Destination 00000000 và cờ RTF_GATEWAY; Gateway ghi hex little-endian.
func parseRoute(r io.Reader) (netip.Addr, error) {
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&0x2 == 0 {
			continue
		}
		gw, err := strconv.ParseUint(f[2], 16, 32)
		if err != nil || gw == 0 {
			continue
		}
		var a [4]byte
		binary.LittleEndian.PutUint32(a[:], uint32(gw))
		return netip.AddrFrom4(a), nil
	}
	return netip.Addr{}, errors.New("không có router mặc định")
}

// Ping: một gói ICMP echo tới ip (socket ping không đặc quyền; ErrUnsupported nếu máy không cho). Dùng lại
// cho kiểm kê LAN.
func Ping(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, error) {
	return ping(ctx, ip, timeout)
}

// DialTCP: mở được kết nối TCP tới ip:port trong timeout. Dùng lại cho kiểm kê LAN.
func DialTCP(ctx context.Context, ip netip.Addr, port int, timeout time.Duration) (time.Duration, error) {
	return dialTCP(ctx, ip, port, timeout)
}

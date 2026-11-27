// Package lan: kiểm kê thiết bị LAN theo sơ đồ LAN của bộ rule đã ký (schema 4).
//
// Ngay sau heartbeat thành công đầu tiên kể từ khi agent chạy, rồi sau mỗi every_hb heartbeat thành công (hoặc số
// ngẫu nhiên trong [every_hb, every_hb_max] — schema 5, bốc lại sau mỗi lượt), agent
// kiểm lần lượt từng thiết bị trong sơ đồ (ping ICMP không đặc quyền hoặc mở kết nối TCP, thử lại khi không
// đáp) rồi giữ kết quả dạng bitmap; heartbeat kế tiếp gửi kèm, server nhận thì bỏ. Không sinh sự kiện.
//
// Chỉ kiểm thiết bị nằm cùng dải mạng với một card mạng của chính máy: bộ rule dù bị sửa cũng không biến
// agent thành công cụ quét mạng khác. Máy tắt kiểm thiết bị (SCS_PROBES=off) thì chỉ báo trạng thái
// probes_off, không gửi gói nào.
package lan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/probe"
	"github.com/thangkes/SCS-System/agent/internal/scan"
	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/common/rules"
)

// retryGap: chờ trước lần thử lại một thiết bị không đáp.
const retryGap = 300 * time.Millisecond

// Inventory: kiểm kê LAN, gắn với vòng heartbeat (Pending trước khi gửi, HeartbeatOK sau khi gửi thành công).
type Inventory struct {
	Log        *slog.Logger
	StateDir   string
	ReleasePub string
	Enabled    bool // SCS_PROBES (false = chỉ báo probes_off)

	// Đổi được khi test.
	Load     func() (*rules.RuleSet, error)
	Ping     func(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, error)
	Dial     func(ctx context.Context, ip netip.Addr, port int, timeout time.Duration) (time.Duration, error)
	Prefixes func() ([]netip.Prefix, error) // các dải IPv4 của card mạng trên máy (địa chỉ máy + độ dài mặt nạ)
	Now      func() time.Time
	Sleep    func(ctx context.Context, d time.Duration)
	Intn     func(n int) int // ngẫu nhiên [0, n) cho nhịp every_hb_max

	mu      sync.Mutex
	ok      int // số heartbeat thành công từ khi chạy
	next    int // lượt kiểm kế tiếp chạy khi ok ≥ next (0 = ngay heartbeat thành công đầu tiên)
	round   int
	running bool
	pending *protocol.LANReport
	wg      sync.WaitGroup
	once    sync.Once
}

func (v *Inventory) defaults() { v.once.Do(v.fill) }

func (v *Inventory) fill() {
	if v.Load == nil {
		v.Load = func() (*rules.RuleSet, error) { return scan.Load(v.StateDir, v.ReleasePub) }
	}
	if v.Ping == nil {
		v.Ping = probe.Ping
	}
	if v.Dial == nil {
		v.Dial = probe.DialTCP
	}
	if v.Prefixes == nil {
		v.Prefixes = localPrefixes
	}
	if v.Now == nil {
		v.Now = time.Now
	}
	if v.Intn == nil {
		v.Intn = rand.IntN
	}
	if v.Sleep == nil {
		v.Sleep = func(ctx context.Context, d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		}
	}
}

// Pending: kết quả lượt kiểm chưa gửi được (nil = không có). Heartbeat gắn vào body.
func (v *Inventory) Pending() *protocol.LANReport {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.pending == nil {
		return nil
	}
	c := *v.pending
	return &c
}

// HeartbeatOK: heartbeat vừa gửi thành công, mang theo sent (nil nếu không mang). Bỏ kết quả server đã nhận;
// tới lượt thì chạy một lượt kiểm ở nền (kết quả gửi ở heartbeat sau).
func (v *Inventory) HeartbeatOK(ctx context.Context, sent *protocol.LANReport) {
	if v == nil {
		return
	}
	v.defaults()
	v.mu.Lock()
	if sent != nil && v.pending != nil && v.pending.Round == sent.Round {
		v.pending = nil
	}
	v.ok++
	n, busy := v.ok, v.running
	v.mu.Unlock()
	if busy {
		return
	}
	set, err := v.Load()
	if err != nil || set == nil || set.LAN == nil {
		if err != nil && !errors.Is(err, scan.ErrNoBundle) && v.Log != nil {
			v.Log.Debug("không đọc được bộ rule cho kiểm kê LAN", "err", err)
		}
		return
	}
	l := *set.LAN
	v.mu.Lock()
	if v.next != 0 && n < v.next {
		v.mu.Unlock()
		return
	}
	// Lượt sau: sau every_hb heartbeat, hoặc ngẫu nhiên trong [every_hb, every_hb_max] (schema 5). Lượt trước còn
	// chạy lúc tới hạn thì chạy ngay heartbeat kế tiếp sau khi xong.
	v.next = n + l.NextEvery(v.Intn)
	v.running = true
	v.round++
	round := v.round
	v.mu.Unlock()
	v.wg.Add(1)
	go func() {
		defer v.wg.Done()
		r := v.Round(ctx, &l)
		r.Round = round
		v.mu.Lock()
		v.running = false
		if ctx.Err() == nil {
			v.pending = r
		}
		v.mu.Unlock()
		if v.Log != nil {
			v.Log.Info("đã kiểm thiết bị LAN", "trang_thai", r.State, "co_mat", popcount(r.Up), "tong", r.N,
				"khong_kiem", popcount(r.Skip), "mat", time.Duration(r.DurMs)*time.Millisecond)
		}
	}()
}

// Wait: chờ lượt kiểm đang chạy (test).
func (v *Inventory) Wait() { v.wg.Wait() }

// Round: kiểm một lượt cả sơ đồ, tuần tự từng thiết bị.
func (v *Inventory) Round(ctx context.Context, l *rules.LAN) *protocol.LANReport {
	v.defaults()
	start := v.Now()
	r := &protocol.LANReport{ListV: l.ListV(), N: len(l.Targets), State: protocol.LANStateOK, At: start.UTC()}
	var up, skip uint64
	all := uint64(1)<<len(l.Targets) - 1
	if len(l.Targets) >= 64 {
		all = ^uint64(0)
	}
	defer func() {
		r.Up, r.Skip = hex64(up), ""
		if skip != 0 {
			r.Skip = hex64(skip)
		}
		r.DurMs = int(v.Now().Sub(start) / time.Millisecond)
	}()
	if !v.Enabled {
		r.State, skip = protocol.LANStateProbesOff, all
		return r
	}
	prefixes, _ := v.Prefixes()
	own, inLAN := pickLAN(prefixes, l)
	if !own.IsValid() {
		r.State, skip = protocol.LANStateNoLAN, all
		return r
	}
	r.LANIP = own.String()
	timeout := time.Duration(min(max(l.Timeout(), 100), rules.MaxProbeTimeoutMs)) * time.Millisecond
	icmpDenied := false
	for i, t := range l.Targets {
		bit := uint64(1) << i
		ip := t.Addr()
		if ctx.Err() != nil {
			skip |= all &^ (up | skip) &^ (bit - 1)
			return r
		}
		if !ip.IsValid() || !rules.LANAddrAllowed(ip) || !inLAN(ip) {
			skip |= bit
			continue
		}
		if ip == own && t.KindOf() == "icmp" { // chính máy này: đang chạy thì có mặt
			up |= bit
			continue
		}
		if t.KindOf() == "icmp" && icmpDenied {
			skip |= bit
			continue
		}
		var err error
		for try := 0; try < l.Tries(); try++ {
			if try > 0 {
				v.Sleep(ctx, retryGap)
			}
			switch t.KindOf() {
			case "tcp":
				_, err = v.Dial(ctx, ip, t.Port, timeout)
			default:
				_, err = v.Ping(ctx, ip, timeout)
			}
			if err == nil || errors.Is(err, probe.ErrUnsupported) || ctx.Err() != nil {
				break
			}
		}
		switch {
		case err == nil:
			up |= bit
		case errors.Is(err, probe.ErrUnsupported):
			icmpDenied = true
			skip |= bit
		}
	}
	if icmpDenied {
		r.State = protocol.LANStateICMPDenied
		if v.Log != nil {
			v.Log.Warn("máy không cho mở socket ping (net.ipv4.ping_group_range): thiết bị kiểm bằng ping bị bỏ qua")
		}
	}
	return r
}

// pickLAN: địa chỉ của máy trong dải chứa nhiều thiết bị của sơ đồ nhất, và hàm "IP này cùng dải với máy".
func pickLAN(prefixes []netip.Prefix, l *rules.LAN) (netip.Addr, func(netip.Addr) bool) {
	best, bestN := netip.Prefix{}, 0
	for _, p := range prefixes {
		if !p.Addr().Is4() {
			continue
		}
		n := 0
		for _, t := range l.Targets {
			if p.Masked().Contains(t.Addr()) {
				n++
			}
		}
		if n > bestN {
			best, bestN = p, n
		}
	}
	if bestN == 0 {
		return netip.Addr{}, func(netip.Addr) bool { return false }
	}
	var mine []netip.Prefix
	for _, p := range prefixes {
		if p.Addr().Is4() {
			mine = append(mine, p.Masked())
		}
	}
	return best.Addr(), func(ip netip.Addr) bool {
		for _, p := range mine {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	}
}

// localPrefixes: các địa chỉ IPv4 (kèm mặt nạ) của card mạng đang bật, trừ loopback.
func localPrefixes() ([]netip.Prefix, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(n.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			ones, bits := n.Mask.Size()
			if !ip.Is4() || bits != 32 || ones < 8 {
				continue // dải /0–/7 quá rộng: không coi là LAN
			}
			out = append(out, netip.PrefixFrom(ip, ones))
		}
	}
	return out, nil
}

func hex64(x uint64) string { return fmt.Sprintf("%016x", x) }

func popcount(h string) int {
	var x uint64
	fmt.Sscanf(h, "%x", &x)
	n := 0
	for ; x != 0; x &= x - 1 {
		n++
	}
	return n
}

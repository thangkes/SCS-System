package rules

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Nguồn "probe" (schema 3): agent tự kiểm thiết bị trong LAN (ping ICMP hoặc mở kết nối TCP) theo đúng
// danh sách rule khai báo, rồi đưa kết quả vào Engine như một dòng log:
//
//	probe icmp 192.168.1.1 down timeout          mỗi lần không đáp
//	probe tcp 192.168.1.50:9100 down refused
//	probe icmp 192.168.1.1 up rtt=3ms            chỉ khi vừa hồi lại sau lần down
//
// Agent chỉ kiểm khi có rule đã ký yêu cầu, trong giới hạn dưới đây (server kiểm lúc ký, agent kiểm lại
// trước khi chạy): chỉ IP nội bộ cố định hoặc {gateway}, không dải IP, không tên miền; tổng số đích cả
// bộ ≤ MaxProbeTargets; nhịp ≥ MinProbeEveryS. Máy tắt hẳn bằng SCS_PROBES=off trong agent.env.

// SourceProbe: nguồn dòng do agent tự kiểm thiết bị (rule cần probe, schema 3).
const SourceProbe = "probe"

// TargetGateway: router mặc định của máy (đọc bảng định tuyến lúc kiểm).
const TargetGateway = "{gateway}"

// Giới hạn kiểm thiết bị.
const (
	MaxProbeTargets    = 16 // tổng số đích (kiểu + đích + cổng) khác nhau trong cả bộ rule
	MinProbeEveryS     = 60 // nhịp kiểm nhỏ nhất
	MaxProbeEveryS     = 86400
	DefaultProbeEveryS = 120
	MaxProbeTimeoutMs  = 5000
	DefaultProbeTimeMs = 1500
	ProbeSchema        = 3
)

// Probe: cách kiểm của một rule nguồn probe.
type Probe struct {
	Kind      string   `json:"kind"`                 // icmp | tcp
	Targets   []string `json:"targets"`              // IP nội bộ (IPv4) hoặc {gateway}
	Port      int      `json:"port,omitempty"`       // tcp: cổng cần mở được
	EveryS    int      `json:"every_s,omitempty"`    // nhịp kiểm (mặc định 120, tối thiểu 60)
	TimeoutMs int      `json:"timeout_ms,omitempty"` // chờ trả lời (mặc định 1500, tối đa 5000)
}

// Every / Timeout: giá trị dùng khi chạy (đã áp mặc định).
func (p *Probe) Every() int {
	if p.EveryS == 0 {
		return DefaultProbeEveryS
	}
	return p.EveryS
}

func (p *Probe) Timeout() int {
	if p.TimeoutMs == 0 {
		return DefaultProbeTimeMs
	}
	return p.TimeoutMs
}

// Key: khóa của một đích (dùng đếm giới hạn và giữ trạng thái up / down).
func (p *Probe) Key(target string) string {
	if p.Kind == "tcp" {
		return "tcp " + target + ":" + strconv.Itoa(p.Port)
	}
	return "icmp " + target
}

// ProbeAllowed: IP được phép kiểm — IPv4 nội bộ (10/8, 172.16/12, 192.168/16). Agent gọi lại cho IP thật
// của {gateway} lúc chạy.
func ProbeAllowed(ip netip.Addr) bool {
	return ip.Is4() && ip.IsPrivate()
}

func (p *Probe) validate() []string {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	switch p.Kind {
	case "icmp":
		if p.Port != 0 {
			add("probe.port: chỉ dùng với kind tcp")
		}
	case "tcp":
		if p.Port < 1 || p.Port > 65535 {
			add("probe.port: kind tcp cần cổng 1–65535")
		}
	default:
		add("probe.kind %q: icmp hoặc tcp", p.Kind)
	}
	if len(p.Targets) == 0 || len(p.Targets) > MaxProbeTargets {
		add("probe.targets: 1 tới %d đích", MaxProbeTargets)
	}
	for i, t := range p.Targets {
		if slices.Contains(p.Targets[:i], t) {
			add("probe.targets %q: bị trùng", t)
			continue
		}
		if t == TargetGateway {
			continue
		}
		ip, err := netip.ParseAddr(t)
		if err != nil || ip.String() != t || strings.Contains(t, "/") {
			add("probe.targets %q: chỉ một IPv4 cố định (không dải IP, không tên miền) hoặc %s", t, TargetGateway)
			continue
		}
		if !ProbeAllowed(ip) {
			add("probe.targets %q: chỉ IP nội bộ (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)", t)
		}
	}
	if p.EveryS != 0 && (p.EveryS < MinProbeEveryS || p.EveryS > MaxProbeEveryS) {
		add("probe.every_s: từ %d tới %d giây", MinProbeEveryS, MaxProbeEveryS)
	}
	if p.TimeoutMs != 0 && (p.TimeoutMs < 100 || p.TimeoutMs > MaxProbeTimeoutMs) {
		add("probe.timeout_ms: từ 100 tới %d", MaxProbeTimeoutMs)
	}
	return errs
}

// ProbeSpec: một rule kiểm thiết bị agent phải chạy.
type ProbeSpec struct {
	RuleID string
	Probe  Probe
}

// Probes: các rule nguồn probe đang chạy trên máy này (sau lọc applies_to), sắp theo mã rule.
func (e *Engine) Probes() []ProbeSpec {
	var out []ProbeSpec
	for _, c := range e.bySource[SourceProbe] {
		if c.Probe != nil {
			out = append(out, ProbeSpec{RuleID: c.ID, Probe: *c.Probe})
		}
	}
	slices.SortFunc(out, func(a, b ProbeSpec) int { return strings.Compare(a.RuleID, b.RuleID) })
	return out
}

// probeTargets: số đích khác nhau của mọi rule probe đang bật trong bộ.
func (s *RuleSet) probeTargets() int {
	seen := map[string]bool{}
	for _, r := range s.Rules {
		if r.Disabled || r.Probe == nil {
			continue
		}
		for _, t := range r.Probe.Targets {
			seen[r.Probe.Key(t)] = true
		}
	}
	return len(seen)
}

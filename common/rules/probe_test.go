package rules

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func probeSet(rs ...Rule) *RuleSet {
	return &RuleSet{Schema: 3, Name: "net", Version: "1", Rules: rs}
}

func probeRule(id string, p *Probe) Rule {
	return Rule{ID: id, Severity: "high", Source: SourceProbe, Probe: p, Match: `probe \S+ (?P<ip>\S+) down`}
}

// Giới hạn kiểm thiết bị: chỉ IP nội bộ cố định / {gateway}, không dải IP / tên miền / IP công khai,
// tổng ≤ 16 đích cả bộ, nhịp ≥ 60 giây, cần schema 3, probe chỉ đi với source probe.
func TestProbeValidate(t *testing.T) {
	ok := probeSet(
		probeRule("NET-GW-01", &Probe{Kind: "icmp", Targets: []string{TargetGateway, "192.168.1.1"}}),
		probeRule("NET-PRN-01", &Probe{Kind: "tcp", Targets: []string{"10.0.5.20"}, Port: 9100, EveryS: 300, TimeoutMs: 2000}),
	)
	if err := ok.Validate(); err != nil {
		t.Fatalf("bộ hợp lệ bị từ chối: %v", err)
	}
	bad := []struct {
		name string
		set  *RuleSet
		want string
	}{
		{"IP công khai", probeSet(probeRule("A-01", &Probe{Kind: "icmp", Targets: []string{"8.8.8.8"}})), "IP nội bộ"},
		{"dải IP", probeSet(probeRule("A-01", &Probe{Kind: "icmp", Targets: []string{"192.168.1.0/24"}})), "không dải IP"},
		{"tên miền", probeSet(probeRule("A-01", &Probe{Kind: "icmp", Targets: []string{"router.local"}})), "không dải IP"},
		{"IPv6", probeSet(probeRule("A-01", &Probe{Kind: "icmp", Targets: []string{"fd00::1"}})), "IP nội bộ"},
		{"nhịp quá dày", probeSet(probeRule("A-01", &Probe{Kind: "icmp", Targets: []string{"10.0.0.1"}, EveryS: 10})), "every_s"},
		{"timeout quá dài", probeSet(probeRule("A-01", &Probe{Kind: "icmp", Targets: []string{"10.0.0.1"}, TimeoutMs: 60000})), "timeout_ms"},
		{"tcp thiếu cổng", probeSet(probeRule("A-01", &Probe{Kind: "tcp", Targets: []string{"10.0.0.1"}})), "cổng"},
		{"kiểu lạ", probeSet(probeRule("A-01", &Probe{Kind: "udp", Targets: []string{"10.0.0.1"}})), "icmp hoặc tcp"},
		{"thiếu probe", probeSet(probeRule("A-01", nil)), "thiếu probe"},
		{"schema 2", &RuleSet{Schema: 2, Name: "n", Version: "1", Rules: []Rule{probeRule("A-01", &Probe{Kind: "icmp", Targets: []string{"10.0.0.1"}})}}, "schema: 3"},
		{"probe ở rule file", probeSet(Rule{ID: "A-01", Severity: "low", Source: "/var/log/syslog", Match: "x",
			Probe: &Probe{Kind: "icmp", Targets: []string{"10.0.0.1"}}}), "chỉ dùng khi source là probe"},
	}
	for _, c := range bad {
		err := c.set.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: cần lỗi chứa %q, được %v", c.name, c.want, err)
		}
	}
	// Tổng số đích cả bộ ≤ 16 (gộp nhiều rule).
	var rs []Rule
	for i := 0; i < 3; i++ {
		var tg []string
		for j := 0; j < 6; j++ {
			tg = append(tg, fmt.Sprintf("10.0.%d.%d", i, j+1))
		}
		rs = append(rs, probeRule(fmt.Sprintf("NET-%02d", i), &Probe{Kind: "icmp", Targets: tg}))
	}
	if err := probeSet(rs...).Validate(); err == nil || !strings.Contains(err.Error(), "tối đa 16") {
		t.Fatalf("18 đích cả bộ phải bị từ chối: %v", err)
	}
}

// tests của rule probe chạy được khi ký (dòng ví dụ mang mã rule).
func TestProbeRuleTests(t *testing.T) {
	r := probeRule("NET-GW-01", &Probe{Kind: "icmp", Targets: []string{TargetGateway}})
	r.Tests = &Tests{Match: []string{"probe icmp 192.168.1.1 down timeout"}, NoMatch: []string{"probe icmp 192.168.1.1 up rtt=2ms"}}
	if fails := probeSet(r).RunTests(); len(fails) != 0 {
		t.Fatalf("tests rule probe: %v", fails)
	}
}

// Kết quả kiểm chỉ đi vào đúng rule đã yêu cầu; nguồn probe không thành nguồn log cần đọc.
func TestProbeEngine(t *testing.T) {
	set := probeSet(
		probeRule("NET-GW-01", &Probe{Kind: "icmp", Targets: []string{TargetGateway}}),
		probeRule("NET-PRN-01", &Probe{Kind: "tcp", Targets: []string{"10.0.5.20"}, Port: 9100}),
		Rule{ID: "SSH-01", Severity: "low", Source: SourceJournald, Match: "sshd"},
	)
	e, err := Compile(set)
	if err != nil {
		t.Fatal(err)
	}
	if src := e.Sources(); len(src) != 1 || src[0].Source != SourceJournald {
		t.Fatalf("nguồn log: %+v", src)
	}
	if p := e.Probes(); len(p) != 2 || p[0].RuleID != "NET-GW-01" || p[1].Probe.Port != 9100 || p[0].Probe.Every() != DefaultProbeEveryS {
		t.Fatalf("probes: %+v", p)
	}
	now := time.Now()
	evs := e.Feed(Line{Source: SourceProbe, Unit: "NET-PRN-01", Time: now, Text: "probe icmp 192.168.1.1 down timeout"})
	if len(evs) != 1 || evs[0].RuleID != "NET-PRN-01" {
		t.Fatalf("chỉ rule đã yêu cầu nhận dòng: %+v", evs)
	}
	if evs := e.Feed(Line{Source: SourceProbe, Unit: "SSH-01", Time: now, Text: "probe icmp 10.0.0.1 down"}); len(evs) != 0 {
		t.Fatalf("mã rule không phải probe: không rule nào nhận: %+v", evs)
	}
}

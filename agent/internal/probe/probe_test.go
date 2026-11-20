package probe

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/rules"
)

func collect(ch chan rules.Line) []string {
	var out []string
	for {
		select {
		case l := <-ch:
			out = append(out, l.Unit+"|"+l.Text)
		default:
			return out
		}
	}
}

// Mỗi lần không đáp ghi một dòng down; dòng up chỉ khi vừa hồi lại; {gateway} ghi IP thật; IP công khai
// (kể cả gateway trả về IP công khai) không bao giờ bị kiểm; máy không hỗ trợ thì không sinh dòng.
func TestRound(t *testing.T) {
	ch := make(chan rules.Line, 32)
	alive := map[string]bool{}
	pings := 0
	r := &Runner{Out: ch,
		Ping: func(_ context.Context, ip netip.Addr, _ time.Duration) (time.Duration, error) {
			pings++
			if alive[ip.String()] {
				return 3 * time.Millisecond, nil
			}
			return 0, errors.New("timeout")
		},
		Dial: func(_ context.Context, ip netip.Addr, port int, _ time.Duration) (time.Duration, error) {
			return 0, errors.New("connect: connection refused")
		},
		Gateway: func() (netip.Addr, error) { return netip.MustParseAddr("192.168.1.1"), nil },
	}
	gw := rules.ProbeSpec{RuleID: "NET-GW-01", Probe: rules.Probe{Kind: "icmp", Targets: []string{rules.TargetGateway, "10.0.0.9"}}}
	prn := rules.ProbeSpec{RuleID: "NET-PRN-01", Probe: rules.Probe{Kind: "tcp", Targets: []string{"10.0.5.20"}, Port: 9100}}
	ctx := context.Background()

	r.Round(ctx, gw)
	r.Round(ctx, prn)
	got := collect(ch)
	want := []string{"NET-GW-01|probe icmp 192.168.1.1 down timeout", "NET-GW-01|probe icmp 10.0.0.9 down timeout",
		"NET-PRN-01|probe tcp 10.0.5.20:9100 down refused"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lượt 1:\n%s", strings.Join(got, "\n"))
	}
	alive["192.168.1.1"] = true
	r.Round(ctx, gw)
	r.Round(ctx, gw)
	got = collect(ch)
	if len(got) != 3 || !strings.HasPrefix(got[0], "NET-GW-01|probe icmp 192.168.1.1 up rtt=") || got[1] != "NET-GW-01|probe icmp 10.0.0.9 down timeout" ||
		got[2] != "NET-GW-01|probe icmp 10.0.0.9 down timeout" {
		t.Fatalf("up chỉ một lần khi vừa hồi lại, down mỗi lần:\n%s", strings.Join(got, "\n"))
	}

	// Gateway là IP công khai: không kiểm, không sinh dòng.
	pings = 0
	r.Gateway = func() (netip.Addr, error) { return netip.MustParseAddr("8.8.8.8"), nil }
	r.Round(ctx, rules.ProbeSpec{RuleID: "NET-GW-01", Probe: rules.Probe{Kind: "icmp", Targets: []string{rules.TargetGateway}}})
	if pings != 0 || len(collect(ch)) != 0 {
		t.Fatalf("IP công khai không được kiểm (ping %d lần)", pings)
	}
	// Máy không hỗ trợ ping: không sinh dòng down giả.
	r.Ping = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) { return 0, ErrUnsupported }
	r.Round(ctx, rules.ProbeSpec{RuleID: "NET-X-01", Probe: rules.Probe{Kind: "icmp", Targets: []string{"10.0.0.1"}}})
	if got := collect(ch); len(got) != 0 {
		t.Fatalf("không hỗ trợ thì không sinh dòng: %v", got)
	}
}

func TestParseRoute(t *testing.T) {
	const tbl = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\n" +
		"eth0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\n" +
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\n"
	ip, err := parseRoute(strings.NewReader(tbl))
	if err != nil || ip.String() != "192.168.1.1" {
		t.Fatalf("gateway: %v %v", ip, err)
	}
	if _, err := parseRoute(strings.NewReader("Iface\tDestination\n")); err == nil {
		t.Fatal("không có router mặc định phải báo lỗi")
	}
}

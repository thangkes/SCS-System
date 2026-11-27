package lan

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/probe"
	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/common/rules"
)

func testLAN() *rules.LAN {
	return &rules.LAN{EveryHB: 3, Targets: []rules.LANTarget{
		{IP: "192.168.1.10", Type: "can"},                             // bit 0: đáp
		{IP: "192.168.1.11", Type: "can"},                             // bit 1: không đáp
		{IP: "192.168.1.31", Type: "tn"},                              // bit 2: chính máy này
		{IP: "192.168.1.50", Type: "may_in", Kind: "tcp", Port: 9100}, // bit 3: cổng mở ở lần thử 2
		{IP: "10.9.9.9", Type: "khac"},                                // bit 4: khác dải mạng của máy → không kiểm
	}}
}

func fakeInv(pings *atomic.Int32) *Inventory {
	dials := 0
	return &Inventory{
		Enabled:  true,
		Prefixes: func() ([]netip.Prefix, error) { return []netip.Prefix{netip.MustParsePrefix("192.168.1.31/24")}, nil },
		Ping: func(_ context.Context, ip netip.Addr, _ time.Duration) (time.Duration, error) {
			pings.Add(1)
			if ip.String() == "10.9.9.9" {
				panic("không được ping IP ngoài dải mạng của máy")
			}
			if ip.String() == "192.168.1.10" {
				return time.Millisecond, nil
			}
			return 0, errors.New("timeout")
		},
		Dial: func(context.Context, netip.Addr, int, time.Duration) (time.Duration, error) {
			dials++
			if dials < 2 {
				return 0, errors.New("refused")
			}
			return time.Millisecond, nil
		},
		Sleep: func(context.Context, time.Duration) {},
	}
}

func TestRound(t *testing.T) {
	var pings atomic.Int32
	v := fakeInv(&pings)
	l := testLAN()
	r := v.Round(context.Background(), l)
	if r.State != protocol.LANStateOK || r.N != 5 || r.ListV != l.ListV() || r.LANIP != "192.168.1.31" {
		t.Fatalf("báo cáo sai: %+v", r)
	}
	if r.Up != "000000000000000d" { // bit 0, 2, 3
		t.Errorf("up = %s, muốn 000000000000000d", r.Up)
	}
	if r.Skip != "0000000000000010" { // bit 4
		t.Errorf("skip = %s", r.Skip)
	}
	// .10 đáp ngay (1), .11 không đáp: thử 2 lần (mặc định retry 1); chính máy và IP ngoài dải: không ping.
	if pings.Load() != 3 {
		t.Errorf("số lần ping = %d, muốn 3", pings.Load())
	}
}

func TestRoundStates(t *testing.T) {
	var pings atomic.Int32
	l := testLAN()

	off := fakeInv(&pings)
	off.Enabled = false
	if r := off.Round(context.Background(), l); r.State != protocol.LANStateProbesOff || r.Up != "0000000000000000" || r.Skip != "000000000000001f" {
		t.Errorf("SCS_PROBES=off: %+v", r)
	}
	nolan := fakeInv(&pings)
	nolan.Prefixes = func() ([]netip.Prefix, error) { return []netip.Prefix{netip.MustParsePrefix("172.16.5.2/24")}, nil }
	if r := nolan.Round(context.Background(), l); r.State != protocol.LANStateNoLAN || r.Skip != "000000000000001f" {
		t.Errorf("không cùng dải: %+v", r)
	}
	if pings.Load() != 0 {
		t.Fatalf("tắt / khác dải mà vẫn ping %d lần", pings.Load())
	}
	denied := fakeInv(&pings)
	denied.Ping = func(context.Context, netip.Addr, time.Duration) (time.Duration, error) {
		pings.Add(1)
		return 0, probe.ErrUnsupported
	}
	r := denied.Round(context.Background(), l)
	if r.State != protocol.LANStateICMPDenied {
		t.Errorf("không có quyền ping: %+v", r)
	}
	if pings.Load() != 1 {
		t.Errorf("không có quyền ping thì chỉ thử 1 lần rồi bỏ các đích ping còn lại, đã thử %d", pings.Load())
	}
	if r.Up != "000000000000000c" || r.Skip != "0000000000000013" { // up: chính máy + tcp; skip: .10 .11 ngoài dải
		t.Errorf("up=%s skip=%s", r.Up, r.Skip)
	}
}

// Lượt đầu chạy sau heartbeat thành công đầu tiên, rồi mỗi EveryHB; kết quả giữ tới khi gửi được.
func TestHeartbeatCycle(t *testing.T) {
	var pings atomic.Int32
	v := fakeInv(&pings)
	l := testLAN()
	v.Load = func() (*rules.RuleSet, error) { return &rules.RuleSet{Schema: 4, LAN: l}, nil }
	ctx := context.Background()
	if v.Pending() != nil {
		t.Fatal("chưa kiểm mà đã có kết quả")
	}
	v.HeartbeatOK(ctx, nil) // 1: kiểm
	v.Wait()
	p := v.Pending()
	if p == nil || p.Round != 1 {
		t.Fatalf("sau heartbeat đầu phải có lượt 1: %+v", p)
	}
	v.HeartbeatOK(ctx, nil) // 2: gửi lỗi trước đó (không mang) → vẫn giữ
	v.Wait()
	if v.Pending() == nil {
		t.Fatal("chưa gửi được mà kết quả bị bỏ")
	}
	v.HeartbeatOK(ctx, p) // 3: server đã nhận
	v.Wait()
	if v.Pending() != nil {
		t.Fatal("server đã nhận mà kết quả vẫn còn")
	}
	v.HeartbeatOK(ctx, nil) // 4 = 1 + EveryHB(3): lượt 2
	v.Wait()
	if p := v.Pending(); p == nil || p.Round != 2 {
		t.Fatalf("heartbeat thứ 4 phải kiểm lượt 2: %+v", p)
	}
	// Bộ rule không có sơ đồ LAN: không kiểm.
	v2 := fakeInv(&pings)
	v2.Load = func() (*rules.RuleSet, error) { return &rules.RuleSet{Schema: 3}, nil }
	v2.HeartbeatOK(ctx, nil)
	v2.Wait()
	if v2.Pending() != nil {
		t.Fatal("không có sơ đồ mà vẫn kiểm")
	}
}

func TestPopcount(t *testing.T) {
	if popcount("000000000000000d") != 3 || popcount("") != 0 {
		t.Fatal("popcount sai")
	}
}

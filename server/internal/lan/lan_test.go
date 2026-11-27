package lan

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/common/rules"
)

// Sơ đồ mẫu (IP trung tính, không phải dải của công ty nào).
func testMap() *rules.LAN {
	return &rules.LAN{Targets: []rules.LANTarget{
		{IP: "192.168.1.10", Type: "can", Label: "Cân 1"},    // 0
		{IP: "192.168.1.11", Type: "can", Label: "Cân 2"},    // 1
		{IP: "192.168.1.31", Type: "tn", Label: "TN1"},       // 2
		{IP: "192.168.1.32", Type: "tn", Label: "TN2"},       // 3
		{IP: "192.168.1.33", Type: "tn", Label: "TN3"},       // 4
		{IP: "192.168.1.50", Type: "may_in", Label: "In 1"},  // 5
		{IP: "192.168.1.251", Type: "dvr", Label: "Đầu ghi"}, // 6 (siêu thị này không có)
	}}
}

func rep(dev int64, ip string, up uint64, at time.Time) *Reporter {
	m := testMap()
	return &Reporter{DeviceID: dev, Store: "100", ListV: m.ListV(), ReceivedAt: at, State: protocol.LANStateOK,
		LANIP: ip, N: len(m.Targets), Up: up}
}

func TestParseAllowAndSignCheck(t *testing.T) {
	allow, err := ParseAllow("192.168.1.0/24, 10.20.0.0/16")
	if err != nil || len(allow) != 2 {
		t.Fatalf("ParseAllow: %v %v", allow, err)
	}
	for _, bad := range []string{"8.8.8.0/24", "192.168.1.5/24", "10.0.0.0/4", "abc"} {
		if _, err := ParseAllow(bad); err == nil {
			t.Errorf("ParseAllow(%q) phải lỗi", bad)
		}
	}
	set := &rules.RuleSet{Schema: 4, Name: "x", Version: "1", LAN: testMap()}
	if err := SignCheck(nil)(set); err == nil || !strings.Contains(err.Error(), "SCS_LAN_ALLOW") {
		t.Errorf("chưa khai dải mà vẫn cho ký: %v", err)
	}
	if err := SignCheck(allow)(set); err != nil {
		t.Errorf("đích trong dải mà bị từ chối: %v", err)
	}
	if err := SignCheck([]netip.Prefix{netip.MustParsePrefix("192.168.1.0/27")})(set); err == nil {
		t.Error("đích ngoài dải mà vẫn cho ký")
	}
	if err := SignCheck(nil)(&rules.RuleSet{Schema: 3}); err != nil {
		t.Errorf("bộ không có sơ đồ LAN không cần dải: %v", err)
	}
}

func TestParseReport(t *testing.T) {
	m := testMap()
	ok := &protocol.LANReport{ListV: m.ListV(), N: 7, Up: "0000000000000005", Skip: "0000000000000040", State: "ok", LANIP: "192.168.1.31"}
	up, skip, ip, err := ParseReport(ok, m)
	if err != nil || up != 5 || skip != 0x40 || ip != "192.168.1.31" {
		t.Fatalf("ParseReport: %x %x %s %v", up, skip, ip, err)
	}
	bad := []*protocol.LANReport{
		{ListV: "0000000000000000", N: 7, Up: "0000000000000001", State: "ok"},                  // sơ đồ khác
		{ListV: m.ListV(), N: 6, Up: "0000000000000001", State: "ok"},                           // sai số đích
		{ListV: m.ListV(), N: 7, Up: "0000000000000080", State: "ok"},                           // bit ngoài sơ đồ
		{ListV: m.ListV(), N: 7, Up: "1", State: "ok"},                                          // sai dạng
		{ListV: m.ListV(), N: 7, Up: "0000000000000001", Skip: "0000000000000001", State: "ok"}, // vừa up vừa skip
		{ListV: m.ListV(), N: 7, Up: "0000000000000001", State: "lạ"},
		{ListV: m.ListV(), N: 7, Up: "0000000000000001", State: "ok", LANIP: "8.8.8.8"},
	}
	for i, r := range bad {
		if _, _, _, err := ParseReport(r, m); err == nil {
			t.Errorf("báo cáo sai #%d được nhận", i)
		}
	}
}

func TestMergeLifecycle(t *testing.T) {
	m := testMap()
	t0 := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	site := &Site{Store: "100"}

	// Máy A (TN1) lần đầu: thấy cân 1, TN1 (chính nó), TN2, máy in. Lần đầu của siêu thị: không ghi lịch sử.
	a1 := rep(1, "192.168.1.31", 1<<0|1<<2|1<<3|1<<5, t0)
	ch, saveSite, saveRep := Merge(site, a1, nil, m, t0)
	if len(ch) != 0 || !saveSite || !saveRep {
		t.Fatalf("lần đầu: changes=%v saveSite=%v saveRep=%v", ch, saveSite, saveRep)
	}
	site.SavedAt, a1.SavedAt = t0, t0
	if StatusAt(site.Targets["192.168.1.10"], t0) != StOnline || StatusAt(site.Targets["192.168.1.251"], t0) != StNever {
		t.Fatal("trạng thái sau lần đầu sai")
	}

	// 10 phút sau máy B (TN2): cũng thấy như vậy nhưng KHÔNG thấy máy in (máy A vừa thấy) → lệch lượt 1.
	t1 := t0.Add(10 * time.Minute)
	b1 := rep(2, "192.168.1.32", 1<<0|1<<2|1<<3, t1)
	ch, _, _ = Merge(site, b1, nil, m, t1)
	if len(ch) != 0 || b1.Miss["192.168.1.50"] != 1 {
		t.Fatalf("lệch lượt 1: changes=%v miss=%v", ch, b1.Miss)
	}
	// Lượt sau của B vẫn không thấy → miss = 2 (đủ LechRounds).
	t2 := t1.Add(2 * time.Hour)
	a2 := rep(1, "192.168.1.31", 1<<0|1<<2|1<<3|1<<5, t2.Add(-time.Minute))
	Merge(site, a2, a1, m, t2.Add(-time.Minute))
	b2 := rep(2, "192.168.1.32", 1<<0|1<<2|1<<3, t2)
	Merge(site, b2, b1, m, t2)
	if b2.Miss["192.168.1.50"] != 2 {
		t.Fatalf("lệch lượt 2: %v", b2.Miss)
	}

	// Cân 1 tắt: 6 giờ sau không máy nào thấy → vắng (1 lần đổi được ghi).
	t3 := t2.Add(6 * time.Hour)
	a3 := rep(1, "192.168.1.31", 1<<2|1<<3|1<<5, t3)
	ch, _, _ = Merge(site, a3, a2, m, t3)
	got := map[string]string{}
	for _, c := range ch {
		got[c.Key] = c.From + ">" + c.To
	}
	if got["192.168.1.10"] != "online>absent" {
		t.Fatalf("cân 1 phải chuyển vắng: %v", got)
	}
	if _, ok := got["192.168.1.251"]; ok {
		t.Fatal("đầu ghi chưa từng thấy mà có lịch sử")
	}
	// Cân 1 bật lại.
	t4 := t3.Add(time.Hour)
	ch, _, _ = Merge(site, rep(1, "192.168.1.31", 1<<0|1<<2|1<<3|1<<5, t4), a3, m, t4)
	if len(ch) != 1 || ch[0].To != StOnline || ch[0].Key != "192.168.1.10" {
		t.Fatalf("cân 1 có lại: %v", ch)
	}
	// 31 ngày không thấy → lâu không thấy.
	ts := &TargetState{LastSeen: t0}
	if StatusAt(ts, t0.Add(31*24*time.Hour)) != StGone {
		t.Fatal("quá 30 ngày phải là gone")
	}
}

// Chập chờn: tối đa MaxChangesPerDay lần đổi được ghi mỗi ngày, sau đó chỉ đánh dấu flap.
func TestMergeFlapCap(t *testing.T) {
	m := testMap()
	site := &Site{Store: "100", SavedAt: time.Unix(1, 0)}
	now := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	logged := 0
	var prev *Reporter
	for i := 0; i < 20; i++ {
		up := uint64(1 << 2)
		if i%2 == 0 {
			up |= 1 << 0
		}
		r := rep(1, "192.168.1.31", up, now)
		ch, _, _ := Merge(site, r, prev, m, now)
		for _, c := range ch {
			if c.Key == "192.168.1.10" {
				logged++
			}
		}
		prev = r
		now = now.Add(Window/2 + time.Minute) // mỗi lượt cách > nửa cửa sổ: lần không thấy kế tiếp vẫn còn online
		if i%2 == 0 {
			now = now.Add(Window) // đủ xa để chuyển vắng
		}
		if now.Day() != 27 {
			break
		}
	}
	if logged > MaxChangesPerDay {
		t.Fatalf("ghi %d lần đổi trong ngày, tối đa %d", logged, MaxChangesPerDay)
	}
}

// Ghi DB thưa: báo cáo giống hệt trong vòng 1 giờ thì không ghi dòng máy / dòng siêu thị.
func TestMergeSaveThrottle(t *testing.T) {
	m := testMap()
	t0 := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	site := &Site{Store: "100"}
	a := rep(1, "192.168.1.31", 1<<2, t0)
	Merge(site, a, nil, m, t0)
	site.SavedAt, a.SavedAt = t0, t0
	b := rep(1, "192.168.1.31", 1<<2, t0.Add(20*time.Minute))
	_, saveSite, saveRep := Merge(site, b, a, m, t0.Add(20*time.Minute))
	if saveSite || saveRep {
		t.Fatalf("không đổi gì mà vẫn ghi: site=%v rep=%v", saveSite, saveRep)
	}
	c := rep(1, "192.168.1.31", 1<<2, t0.Add(61*time.Minute))
	_, saveSite, saveRep = Merge(site, c, a, m, t0.Add(61*time.Minute))
	if !saveSite || !saveRep {
		t.Fatal("quá 1 giờ phải ghi lại")
	}
}

func TestView(t *testing.T) {
	m := testMap()
	maps := map[string]*rules.LAN{m.ListV(): m}
	t0 := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	site := &Site{Store: "100"}
	a := rep(1, "192.168.1.31", 1<<0|1<<1|1<<2|1<<3|1<<4|1<<5, t0)
	Merge(site, a, nil, m, t0)
	// Máy B thấy rất ít (lỏng dây) → bất thường; máy C (TN3) chạy ở IP lạ .77 → sai IP.
	b := rep(2, "192.168.1.32", 1<<3, t0)
	Merge(site, b, nil, m, t0)
	c := rep(3, "192.168.1.77", 1<<0|1<<1|1<<2|1<<3|1<<4|1<<5, t0)
	Merge(site, c, nil, m, t0)
	devs := []Device{{ID: 1, Hostname: "100-TN1-PRI"}, {ID: 2, Hostname: "100-TN2-SEC"}, {ID: 3, Hostname: "100-TN3"},
		{ID: 4, Hostname: "100-TN4"}}
	v := View(site, []Reporter{*a, *b, *c}, devs, maps, t0.Add(time.Minute))
	if v.Unknown || v.Present != 6 || v.EverSeen != 6 || v.Missing != 0 {
		t.Fatalf("đếm sai: %+v", v)
	}
	var abn, wrong []string
	for _, r := range v.Reporters {
		if r.Abnormal {
			abn = append(abn, r.Hostname)
		}
		if r.WrongIP {
			wrong = append(wrong, r.Hostname)
		}
	}
	if len(abn) != 1 || abn[0] != "100-TN2-SEC" {
		t.Errorf("máy bất thường: %v", abn)
	}
	if len(wrong) != 1 || wrong[0] != "100-TN3" || v.TNWrongIP != 1 {
		t.Errorf("máy sai IP: %v", wrong)
	}
	// TN3 (.33) quét thấy nhưng không máy agent nào báo IP .33 (máy TN3 đang ở .77). Máy TN4 có agent nhưng chưa
	// báo LAN lần nào (agent cũ) → có thể chính là nó: "chưa rõ", không đếm là chưa có agent.
	tn := func(v StoreView) map[string]string {
		out := map[string]string{}
		for _, tv := range v.Targets {
			if tv.Type == "tn" {
				out[tv.IP] = tv.TN
			}
		}
		return out
	}
	if got := tn(v); got["192.168.1.31"] != TNMatch || got["192.168.1.32"] != TNMatch || got["192.168.1.33"] != TNUnsure {
		t.Errorf("đối chiếu TN: %v", got)
	}
	if v.TNScanned != 3 || v.TNAgents != 4 || v.TNNoAgent != 0 {
		t.Errorf("đếm TN: scanned=%d agents=%d noagent=%d", v.TNScanned, v.TNAgents, v.TNNoAgent)
	}
	// Mọi máy agent đều đã báo: .33 chắc chắn là máy chưa có agent.
	v3 := View(site, []Reporter{*a, *b, *c}, devs[:3], maps, t0.Add(time.Minute))
	if got := tn(v3); got["192.168.1.33"] != TNNoAgent || v3.TNNoAgent != 1 {
		t.Errorf("đủ máy báo mà .33 không phải chua_agent: %v", got)
	}
	if len(v.NoReport) != 1 || v.NoReport[0] != "100-TN4" {
		t.Errorf("máy chưa báo: %v", v.NoReport)
	}
	// 6 giờ sau không ai báo → không rõ (thiết bị chưa từng thấy vẫn là never).
	v2 := View(site, []Reporter{*a, *b, *c}, devs, maps, t0.Add(6*time.Hour))
	if !v2.Unknown {
		t.Fatal("không máy nào báo trong 5 giờ phải là không rõ")
	}
	for _, tv := range v2.Targets {
		if (tv.IP == "192.168.1.251") != (tv.Status == StNever) || (tv.IP != "192.168.1.251" && tv.Status != StUnknown) {
			t.Errorf("%s: %s", tv.IP, tv.Status)
		}
	}
	s := Summarize(v)
	if len(s.Dots) != 7 || s.Targets != nil {
		t.Fatalf("Summarize: %d chấm", len(s.Dots))
	}
}

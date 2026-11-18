package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

func heartbeatAs(t *testing.T, a *testAgent, id, version string, body protocol.HeartbeatBody) []byte {
	t.Helper()
	env, err := protocol.NewEnvelope(protocol.TypeHeartbeat, id, a.comp.Hash(), version, body)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := protocol.Sign(a.priv, env)
	b, _ := json.Marshal(s)
	return b
}

// Phân lượt tải: 1 lượt → máy thứ hai không được báo bản mới (và tải thẳng bị 503) tới khi máy đầu tải xong.
func TestDownloadSlots(t *testing.T) {
	d, reg, _ := releaseDeps(t)
	d.Downloads = NewDownloads(1, 1000)
	putDist(t, d.Cfg.AgentDist, "v1.1.0", map[string]string{"amd64": "agent amd64 v1.1.0"})
	if _, err := importDist(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	reg.SetRollout(context.Background(), RingAll, "v1.1.0", "Quản trị")
	a, b := newTestAgent(t, "12345-TN1-PRI"), newTestAgent(t, "12345-TN2-SEC")
	certA, _ := approvedAgent(t, d, reg, a)
	certB, _ := approvedAgent(t, d, reg, b)
	hb := func(ag *testAgent, cert *x509.Certificate, id string, body protocol.HeartbeatBody) protocol.Response {
		t.Helper()
		body.Arch = "amd64"
		w, resp := postHeartbeat(d, cert, heartbeatAs(t, ag, id, "v1.0.0", body))
		if w.Code != 200 {
			t.Fatalf("heartbeat: %d %s", w.Code, w.Body)
		}
		return resp
	}
	download := func(cert *x509.Certificate) int {
		r := httptest.NewRequest("GET", "/v1/updates/v1.1.0?arch=amd64", nil)
		r.SetPathValue("version", "v1.1.0")
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
		w := httptest.NewRecorder()
		updateHandler(d).ServeHTTP(w, r)
		return w.Code
	}

	if hb(a, certA, "1", protocol.HeartbeatBody{}).Update == nil {
		t.Fatal("máy đầu phải được lượt tải")
	}
	if hb(b, certB, "2", protocol.HeartbeatBody{}).Update != nil {
		t.Fatal("hết lượt thì không báo bản mới")
	}
	if c := download(certB); c != 503 {
		t.Fatalf("tải khi hết lượt phải 503, được %d", c)
	}
	if st := d.Downloads.Stats(); st.InUse != 1 || st.Busy != 2 {
		t.Fatalf("thống kê lượt: %+v", st)
	}
	if c := download(certA); c != 200 {
		t.Fatalf("máy giữ lượt tải được, được %d", c)
	}
	if st := d.Downloads.Stats(); st.InUse != 0 || st.Done != 1 {
		t.Fatalf("tải xong phải trả lượt: %+v", st)
	}
	if hb(b, certB, "2", protocol.HeartbeatBody{UpdateError: &protocol.UpdateError{Version: "v1.1.0", Error: "chữ ký sai"}}).Update == nil {
		t.Fatal("có lượt trống thì máy sau được báo")
	}
	if st := d.Downloads.Stats(); st.Failures["v1.1.0"] != 1 {
		t.Fatalf("phải đếm máy báo lỗi: %+v", st.Failures)
	}
	hb(b, certB, "2", protocol.HeartbeatBody{})
	if st := d.Downloads.Stats(); st.Failures["v1.1.0"] != 0 {
		t.Fatal("heartbeat không lỗi thì xóa lỗi cũ")
	}
}

// Lượt bị giữ quá hạn (máy không tải) thì tự thu lại.
func TestDownloadSlotExpires(t *testing.T) {
	g := NewDownloads(1, 20)
	now := time.Now()
	g.now = func() time.Time { return now }
	if !g.Acquire(1) || g.Acquire(2) || !g.Acquire(1) {
		t.Fatal("1 lượt: máy 1 giữ (xin lại vẫn được), máy 2 phải chờ")
	}
	now = now.Add(slotTTL - time.Minute)
	g.Acquire(1) // xin lại không gia hạn
	now = now.Add(2 * time.Minute)
	if !g.Acquire(2) {
		t.Fatal("quá hạn phải thu lại lượt")
	}
}

// Giới hạn băng thông: 1 Mbit/s (125 KB/s), gửi 256 KB (phần cho phép tức thì) + 125 KB ≈ 1 giây.
func TestDownloadThrottle(t *testing.T) {
	g := NewDownloads(4, 1)
	w := httptest.NewRecorder()
	tw := g.throttle(w, httptest.NewRequest("GET", "/", nil))
	start := time.Now()
	if _, err := tw.Write(make([]byte, downloadBurst+125_000)); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < 800*time.Millisecond || el > 3*time.Second {
		t.Fatalf("phải mất khoảng 1 giây, mất %s", el)
	}
	if w.Body.Len() != downloadBurst+125_000 {
		t.Fatal("phải gửi đủ")
	}
	var nilG *Downloads
	if nilG.Stats() != nil || !nilG.Acquire(1) || nilG.throttle(w, nil) != w {
		t.Fatal("nil = không giới hạn")
	}
}

func TestBuildFleet(t *testing.T) {
	now := time.Now()
	ro := Rollout{All: "v2", Test: "v3"}
	rows := []FleetRow{
		{ID: 1, Status: DeviceApproved, Version: "v2", LastSeen: now, RulesReported: true, RulesInstalled: true, Scanning: true},
		{ID: 2, Status: DeviceApproved, Version: "v1", LastSeen: now, RulesReported: true},
		{ID: 3, Status: DeviceApproved, Version: "v3", TestRing: true, LastSeen: now.Add(-2 * time.Hour), RulesReported: true, RulesInstalled: true},
		{ID: 4, Status: DeviceApproved, Version: ""},
		{ID: 5, Status: DevicePending, Version: "v2"},
		{ID: 6, Status: DeviceRevoked, Version: "v1"},
	}
	st := buildFleet(rows, ro, now, time.Hour)
	if st.Approved != 4 || st.OnTarget != 2 || st.Pending != 1 {
		t.Fatalf("tổng: %+v", st)
	}
	g := map[string]FleetGroup{}
	for _, x := range st.Groups {
		g[x.Key] = x
	}
	if a := g["active"]; a.Total != 2 || *a.OnTarget != 1 || *a.NotUpdated != 1 || *a.Rules != 1 || *a.NoRules != 1 {
		t.Fatalf("đang hoạt động: %+v", a)
	}
	if o := g["offline"]; o.Total != 2 || *o.OnTarget != 1 || *o.Rules != 1 {
		t.Fatalf("mất liên lạc (máy 3 nhóm thử đúng bản v3, máy 4 chưa gọi về): %+v", o)
	}
	if p := g["pending"]; p.Total != 1 || *p.OnTarget != 1 || p.Rules != nil {
		t.Fatalf("chờ duyệt: %+v", p)
	}
	if c := g["closed"]; c.Total != 1 || c.OnTarget != nil {
		t.Fatalf("đã đóng: %+v", c)
	}
	if st.Rules.Scanning != 1 || st.Rules.Installed != 1 || st.Rules.None != 1 || st.Rules.NotReported != 1 {
		t.Fatalf("rule: %+v", st.Rules)
	}
	if len(st.Versions) != 4 {
		t.Fatalf("phiên bản: %+v", st.Versions)
	}
}

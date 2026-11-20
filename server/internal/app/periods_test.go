package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/periods"
)

func logPacket(t *testing.T, a *testAgent, deviceID string, evs []protocol.LogEvent) []byte {
	t.Helper()
	env, err := protocol.NewEnvelope(protocol.TypeLogBatch, deviceID, a.comp.Hash(), "0.2.0", protocol.LogBatchBody{Events: evs})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := protocol.Sign(a.priv, env)
	b, _ := json.Marshal(s)
	return b
}

func postLogs(d Deps, cert *x509.Certificate, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/logs", bytes.NewReader(body))
	r.RemoteAddr = "192.168.1.50:40000"
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	w := httptest.NewRecorder()
	logsHandler(d).ServeHTTP(w, r)
	return w
}

func timelineOf(t *testing.T, reg *fakeRegistry, id int64) Timeline {
	t.Helper()
	tl, err := reg.ListPeriods(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// Luồng đầy đủ qua handler: heartbeat mở OK, trạng thái đổi mới ghi lịch sử, sự kiện medium cộng vào
// OK, high mở cảnh báo (kèm dòng trong alerts), admin xác nhận cảnh báo = đóng giai đoạn, mở OK.
func TestPeriodsThroughHandlers(t *testing.T) {
	d, reg := enrollDeps(t)
	a := newTestAgent(t, "12345-TN1-PRI")
	cert, _ := approvedAgent(t, d, reg, a)
	id, _ := strconv.ParseInt(cert.Subject.CommonName, 10, 64)
	dev := cert.Subject.CommonName

	hb := func(svcActive string) {
		t.Helper()
		w, _ := postHeartbeat(d, cert, heartbeatPacket(t, a, dev, func(e *protocol.Envelope) {
			e.Body, _ = json.Marshal(protocol.HeartbeatBody{UptimeS: 60, RuleSetVersion: "bo@1",
				Status: &protocol.MachineStatus{OS: "Ubuntu 22.04", Load: []float64{float64(time.Now().Nanosecond() % 7)},
					Services: []protocol.ServiceStatus{{Name: "cups.service", Active: svcActive}}}})
		}))
		if w.Code != 200 {
			t.Fatalf("heartbeat: %d %s", w.Code, w.Body)
		}
	}
	hb("active")
	hb("active") // tải CPU khác nhưng không đáng kể: không ghi thêm lịch sử
	tl := timelineOf(t, reg, id)
	if len(tl.Periods) != 1 || tl.Periods[0].Kind != periods.KindOK || len(tl.Changes) != 1 {
		t.Fatalf("sau 2 heartbeat: %+v", tl)
	}
	hb("failed") // dịch vụ đổi trạng thái: ghi lịch sử
	if tl = timelineOf(t, reg, id); len(tl.Changes) != 2 || !strings.Contains(string(tl.Changes[0].Changes), `"cups.service":"failed"`) {
		t.Fatalf("lịch sử trạng thái: %+v", tl.Changes)
	}

	now := time.Now().UTC()
	if w := postLogs(d, cert, logPacket(t, a, dev, []protocol.LogEvent{
		{RuleID: "SVC-FAILED-01", Severity: "medium", Time: now, Line: "cups.service: Failed", Count: 1},
		{RuleID: "../lạ", Severity: "medium", Time: now, Count: 1}, // bỏ
		{RuleID: "X-01", Severity: "khẩn", Time: now, Count: 1},    // bỏ
	})); w.Code != 200 || strings.Contains(w.Body.String(), "alert_severity") {
		t.Fatalf("logs medium (máy vẫn OK, không có alert_severity): %d %s", w.Code, w.Body)
	}
	tl = timelineOf(t, reg, id)
	if len(tl.Periods) != 1 || tl.Periods[0].Events != 1 || tl.Periods[0].Rules["SVC-FAILED-01"].Sample != "cups.service: Failed" {
		t.Fatalf("medium phải cộng vào OK: %+v", tl.Periods)
	}

	if w := postLogs(d, cert, logPacket(t, a, dev, []protocol.LogEvent{
		{RuleID: "SSH-ROOT-LOGIN-01", Severity: "high", Time: now, Line: "Accepted password for root\x1b[31m", Count: 1},
	})); w.Code != 200 || !strings.Contains(w.Body.String(), `"alert_severity":"high"`) {
		t.Fatalf("logs high (server báo máy đang cảnh báo high để agent thôi gửi gấp): %d %s", w.Code, w.Body)
	}
	tl = timelineOf(t, reg, id)
	if len(tl.Periods) != 2 || tl.Periods[0].Kind != periods.KindAlert || tl.Periods[1].EndedAt.IsZero() {
		t.Fatalf("high phải mở cảnh báo: %+v", tl.Periods)
	}
	if s := tl.Periods[0].Rules["SSH-ROOT-LOGIN-01"].Sample; strings.ContainsRune(s, 0x1b) {
		t.Fatalf("ký tự điều khiển phải bị bỏ: %q", s)
	}
	alerts, _ := reg.ListAlerts(context.Background(), false)
	var ev *Alert
	for i := range alerts {
		if alerts[i].Kind == "event" {
			ev = &alerts[i]
		}
	}
	if ev == nil || ev.PeriodID != tl.Periods[0].ID || ev.Severity != "high" {
		t.Fatalf("cảnh báo của giai đoạn: %+v", alerts)
	}

	h := AdminHandler(d)
	post := func(path, body string) int {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	// Chờ thông tin / Đã gửi report: bắt buộc ghi chú, trạng thái hợp lệ; sự cố VẪN MỞ.
	pid := strconv.FormatInt(tl.Periods[0].ID, 10)
	if c := post("/admin/v1/periods/"+pid+"/handling", `{"status":"waiting_info","note":"  "}`); c != 400 {
		t.Fatalf("thiếu ghi chú phải bị từ chối: %d", c)
	}
	if c := post("/admin/v1/periods/"+pid+"/handling", `{"status":"done","note":"x"}`); c != 400 {
		t.Fatalf("trạng thái lạ phải bị từ chối: %d", c)
	}
	if c := post("/admin/v1/periods/"+pid+"/resolve", `{"resolution":"maybe"}`); c != 400 {
		t.Fatalf("cách đóng lạ phải bị từ chối: %d", c)
	}
	if c := post("/admin/v1/periods/"+pid+"/handling", `{"status":"reported","note":"đã gửi kỹ thuật\u0007 phiếu 12"}`); c != 200 {
		t.Fatalf("đặt Đã gửi report: %d", c)
	}
	if p := timelineOf(t, reg, id).Periods[0]; !p.Open() || p.Handling != periods.HandlingReported ||
		p.HandlingNote != "đã gửi kỹ thuật phiếu 12" || len(p.HandlingLog) != 1 {
		t.Fatalf("sau Đã gửi report sự cố vẫn mở, có ghi chú (bỏ ký tự điều khiển): %+v", p)
	}

	// Admin xác nhận cảnh báo trên web = "Đã xử lý": đóng giai đoạn cảnh báo, mở OK.
	req := httptest.NewRequest("POST", "/admin/v1/alerts/"+strconv.FormatInt(ev.ID, 10)+"/ack", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("xác nhận: %d %s", rec.Code, rec.Body)
	}
	tl = timelineOf(t, reg, id)
	if len(tl.Periods) != 3 || tl.Periods[0].Kind != periods.KindOK || tl.Periods[1].Kind != periods.KindAlert ||
		!strings.HasPrefix(tl.Periods[1].ClosedBy, "admin:") {
		t.Fatalf("sau khi xử lý: %+v", tl.Periods)
	}

	// API dòng thời gian và "Đã xử lý" khi không có cảnh báo mở.
	req = httptest.NewRequest("GET", "/admin/v1/devices/"+dev+"/periods", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"alert"`) {
		t.Fatalf("API dòng thời gian: %d %s", rec.Code, rec.Body)
	}
	req = httptest.NewRequest("POST", "/admin/v1/periods/"+strconv.FormatInt(tl.Periods[0].ID, 10)+"/resolve", strings.NewReader(`{"note":"x"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Fatalf("xử lý giai đoạn OK phải bị từ chối: %d", rec.Code)
	}

	// Kiểm định kỳ: quá 60 phút không nghe máy → mất liên lạc (kèm cảnh báo), máy gửi lại thì tự đóng.
	if n, _ := reg.PeriodTick(context.Background(), time.Now().Add(70*time.Minute)); n != 1 {
		t.Fatalf("tick: %d", n)
	}
	if tl = timelineOf(t, reg, id); tl.Periods[0].Kind != periods.KindOffline {
		t.Fatalf("mất liên lạc: %+v", tl.Periods[0])
	}
	open := func(kind string) int {
		as, _ := reg.ListAlerts(context.Background(), false)
		n := 0
		for _, x := range as {
			if x.Kind == kind {
				n++
			}
		}
		return n
	}
	if open("offline") != 1 {
		t.Fatal("phải có cảnh báo mất liên lạc")
	}
	hb("failed")
	if tl = timelineOf(t, reg, id); tl.Periods[0].Kind != periods.KindOK || open("offline") != 0 {
		t.Fatalf("máy gửi lại: %+v", tl.Periods[0])
	}
}

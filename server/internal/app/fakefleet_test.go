package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func (f *fakeRegistry) FleetRows(context.Context) ([]FleetRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []FleetRow
	for _, d := range f.devices {
		r := FleetRow{ID: d.ID, Status: d.Status, TestRing: d.TestRing, Version: d.AgentVersion, LastSeen: d.LastSeen}
		if rs := f.status[d.ID].Rules; rs != nil {
			r.RulesReported, r.RulesInstalled, r.Scanning = true, rs.Installed, rs.Scanning
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeRegistry) RecordRolloutProgress(_ context.Context, p ProgressPoint, _ time.Duration) error {
	f.mu.Lock()
	f.progress = append(f.progress, p)
	f.mu.Unlock()
	return nil
}

func (f *fakeRegistry) RolloutProgress(_ context.Context, v string, since time.Time) ([]ProgressPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []ProgressPoint{}
	for _, p := range f.progress {
		if p.Version == v && !p.At.Before(since) {
			out = append(out, p)
		}
	}
	return out, nil
}

// API thống kê agent: đọc ở mức Chỉ xem; ghi mốc tiến độ khi có bản phát hành cho mọi máy.
func TestFleetAPI(t *testing.T) {
	d, reg, _ := releaseDeps(t)
	d.Downloads = NewDownloads(4, 20)
	putDist(t, d.Cfg.AgentDist, "v1.1.0", map[string]string{"amd64": "x"})
	importDist(context.Background(), d)
	a := newTestAgent(t, "12345-TN1-PRI")
	approvedAgent(t, d, reg, a)
	reg.SetRollout(context.Background(), RingAll, "v1.1.0", "Quản trị")
	if err := recordRollout(context.Background(), d, reg, time.Now(), time.Hour); err != nil || len(reg.progress) != 1 ||
		reg.progress[0].Total != 1 || reg.progress[0].OnTarget != 0 {
		t.Fatalf("ghi tiến độ: %v %+v", err, reg.progress)
	}
	r := httptest.NewRequest("GET", "/admin/v1/agent/fleet", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	AdminHandler(d).ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"approved":1`) || !strings.Contains(body, `"slots":4`) ||
		!strings.Contains(body, `"on_target":0,"total":1`) {
		t.Fatalf("API: %d %s", w.Code, body)
	}
}

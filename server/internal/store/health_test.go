package store

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/server/internal/app"
	"github.com/thangkes/SCS-System/server/internal/periods"
)

// Sức khỏe máy trên PostgreSQL thật: lưu health mỗi heartbeat nhưng KHÔNG tạo sự kiện / cảnh báo; danh sách máy
// và dòng thời gian trả health + status; agent không gửi trạng thái thì giữ health cũ.
func TestHealthOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	id := approvedDevice(t, g, "12345-TN2-SEC", "203.0.113.20")
	hb := func(h *app.Health) {
		t.Helper()
		heartbeatWith(t, g, id, "203.0.113.20", h)
	}
	hb(&app.Health{Level: "ok", Issues: []app.HealthIssue{}})
	disk := &app.Health{Level: "bad", Issues: []app.HealthIssue{
		{Code: "disk_full:/", Severity: "critical", Text: "Phân vùng / đầy 96%"}, {Code: "time", Severity: "medium", Text: "Đồng hồ"}}}
	hb(disk)
	var kind string
	var events int
	if err := pool.QueryRow(ctx, `SELECT kind, events FROM device_periods WHERE device_id = $1 AND ended_at IS NULL`, id).Scan(&kind, &events); err != nil {
		t.Fatal(err)
	}
	if kind != periods.KindOK || events != 0 {
		t.Fatalf("sức khỏe không được mở cảnh báo / ghi sự kiện: %s, %d sự kiện", kind, events)
	}
	var alerts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE device_id = $1 AND acked_at IS NULL`, id).Scan(&alerts); err != nil || alerts != 0 {
		t.Fatalf("không được có dòng cảnh báo: %d %v", alerts, err)
	}
	hb(nil) // agent không gửi trạng thái: giữ health cũ
	list, err := g.ListDevices(ctx, "approved")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range list {
		if d.ID == id {
			found = true
			if !(strings.Contains(string(d.Health), `"level": "bad"`) || strings.Contains(string(d.Health), `"level":"bad"`)) {
				t.Fatalf("health trong danh sách máy: %s", d.Health)
			}
		}
	}
	if !found {
		t.Fatal("không thấy máy trong danh sách")
	}
	if tl, err := g.ListPeriods(ctx, id, 0); err != nil || len(tl.Health) == 0 || len(tl.Status) == 0 {
		t.Fatalf("dòng thời gian phải có status + health: %v %s %s", err, tl.Status, tl.Health)
	}
}

// Migration 0017: đóng cảnh báo đang mở chỉ gồm sự kiện sức khỏe "may.*" (bản 6963dd3) cùng dòng alerts của nó;
// cảnh báo có lẫn sự kiện log giữ nguyên; heartbeat sau mở lại giai đoạn OK.
func TestMigration0017ClosesHealthAlerts(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	only := approvedDevice(t, g, "12346-TN1-PRI", "203.0.113.31")
	mixed := approvedDevice(t, g, "12347-TN1-PRI", "203.0.113.32")
	now := time.Now()
	for _, id := range []int64{only, mixed} {
		heartbeatWith(t, g, id, "203.0.113.30", nil)
		// Giả lập bản cũ: sự kiện sức khỏe critical mở cảnh báo.
		if err := g.AddEvents(ctx, id, []periods.Event{{RuleID: "may.fs_ro:/", Severity: "critical", Count: 1, First: now, Last: now, Sample: "chỉ đọc"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.AddEvents(ctx, mixed, []periods.Event{{RuleID: "DISK-IO-ERR-01", Severity: "high", Count: 1, First: now, Last: now, Sample: "I/O error"}}); err != nil {
		t.Fatal(err)
	}
	sql, err := migrationFS.ReadFile("migrations/0017_health_out_of_alerts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	state := func(id int64) (open bool, alertsOpen int) {
		t.Helper()
		err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM device_periods WHERE device_id = $1 AND ended_at IS NULL AND kind = 'alert'),
			(SELECT count(*) FROM alerts WHERE device_id = $1 AND acked_at IS NULL AND period_id IS NOT NULL)`, id).Scan(&open, &alertsOpen)
		if err != nil {
			t.Fatal(err)
		}
		return open, alertsOpen
	}
	if open, n := state(only); open || n != 0 {
		t.Fatalf("cảnh báo chỉ gồm may.* phải đóng (cả dòng alerts): mở=%v, alerts=%d", open, n)
	}
	if open, n := state(mixed); !open || n != 1 {
		t.Fatalf("cảnh báo có lẫn sự kiện log phải giữ nguyên: mở=%v, alerts=%d", open, n)
	}
	heartbeatWith(t, g, only, "203.0.113.31", nil)
	var kind string
	if err := pool.QueryRow(ctx, `SELECT kind FROM device_periods WHERE device_id = $1 AND ended_at IS NULL`, only).Scan(&kind); err != nil || kind != periods.KindOK {
		t.Fatalf("heartbeat sau phải mở giai đoạn OK: %q %v", kind, err)
	}
}

func approvedDevice(t *testing.T, g *Registry, host, ip string) int64 {
	t.Helper()
	res, err := g.Enroll(context.Background(), enrollReq(t, host, ip))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Approve(context.Background(), res.DeviceID, "Quản trị", ""); err != nil {
		t.Fatal(err)
	}
	return res.DeviceID
}

func heartbeatWith(t *testing.T, g *Registry, id int64, ip string, h *app.Health) {
	t.Helper()
	rec := app.HeartbeatRecord{IP: netip.MustParseAddr(ip), AgentVersion: "0.3.0", UptimeS: 60, AgentTime: time.Now(), Health: h}
	if h != nil {
		rec.Status = []byte(`{"os":"Ubuntu","temp_c":50}`)
	}
	if err := g.RecordHeartbeat(context.Background(), id, rec); err != nil {
		t.Fatal(err)
	}
}

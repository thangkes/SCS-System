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

// Sức khỏe máy trên PostgreSQL thật: lưu health mỗi heartbeat; vấn đề MỚI (hoặc tăng mức) ghi một sự kiện
// "may.<mã>" và mở cảnh báo khi high / critical; vấn đề lặp lại không ghi thêm; danh sách máy và dòng thời
// gian trả health + status; agent không gửi trạng thái thì giữ health cũ.
func TestHealthOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	res, err := g.Enroll(ctx, enrollReq(t, "12345-TN2-SEC", "203.0.113.20"))
	if err != nil {
		t.Fatal(err)
	}
	id := res.DeviceID
	if _, err := g.Approve(ctx, id, "Quản trị", ""); err != nil {
		t.Fatal(err)
	}
	hb := func(h *app.Health) {
		t.Helper()
		rec := app.HeartbeatRecord{IP: netip.MustParseAddr("203.0.113.20"), AgentVersion: "0.3.0", UptimeS: 60,
			AgentTime: time.Now(), Health: h}
		if h != nil {
			rec.Status = []byte(`{"os":"Ubuntu","temp_c":50}`)
		}
		if err := g.RecordHeartbeat(ctx, id, rec); err != nil {
			t.Fatal(err)
		}
	}
	events := func(rule string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT coalesce(sum((rules->$2->>'count')::int), 0) FROM device_periods WHERE device_id = $1`,
			id, rule).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ok := &app.Health{Level: "ok", Issues: []app.HealthIssue{}}
	hb(ok)
	time1 := &app.Health{Level: "warn", Issues: []app.HealthIssue{{Code: "time", Severity: "medium", Text: "Đồng hồ"}}}
	hb(time1)
	hb(time1) // lặp lại: không ghi thêm
	if events("may.time") != 1 {
		t.Fatalf("medium mới: 1 sự kiện, được %d", events("may.time"))
	}
	disk := &app.Health{Level: "bad", Issues: []app.HealthIssue{
		{Code: "disk_full:/", Severity: "critical", Text: "Phân vùng / đầy 96%"}, {Code: "time", Severity: "medium", Text: "Đồng hồ"}}}
	hb(disk)
	var kind, sev string
	if err := pool.QueryRow(ctx, `SELECT kind, coalesce(severity, '') FROM device_periods WHERE device_id = $1 AND ended_at IS NULL`, id).Scan(&kind, &sev); err != nil {
		t.Fatal(err)
	}
	if kind != periods.KindAlert || sev != "critical" || events("may.disk_full:/") != 1 || events("may.time") != 1 {
		t.Fatalf("critical mới phải mở cảnh báo: %s %s, %d", kind, sev, events("may.disk_full:/"))
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
			if len(d.Health) == 0 || !(strings.Contains(string(d.Health), `"level": "bad"`) || strings.Contains(string(d.Health), `"level":"bad"`)) {
				t.Fatalf("health trong danh sách máy: %s", d.Health)
			}
		}
	}
	if !found {
		t.Fatal("không thấy máy trong danh sách")
	}
	tl, err := g.ListPeriods(ctx, id, 0)
	if err != nil || len(tl.Health) == 0 || len(tl.Status) == 0 {
		t.Fatalf("dòng thời gian phải có status + health: %v %s %s", err, tl.Status, tl.Health)
	}
	hb(ok) // hết vấn đề: health về ok, không ghi sự kiện mới
	if events("may.disk_full:/") != 1 {
		t.Fatal("hết vấn đề không được ghi thêm sự kiện")
	}
}

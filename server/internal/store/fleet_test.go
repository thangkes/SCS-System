package store

import (
	"context"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/server/internal/app"
)

// Thống kê agent trên PostgreSQL thật: cần SCS_TEST_DATABASE_URL (xem migrate_test.go).
func TestFleetOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	res, err := g.Enroll(ctx, enrollReq(t, "12345-TN1-PRI", "203.0.113.10"))
	if err != nil {
		t.Fatal(err)
	}
	g.Enroll(ctx, enrollReq(t, "12345-TN2-SEC", "203.0.113.10"))
	if _, err := g.Approve(ctx, res.DeviceID, "Quản trị", ""); err != nil {
		t.Fatal(err)
	}
	rows, err := g.FleetRows(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("FleetRows: %v %+v", err, rows)
	}
	for _, r := range rows {
		if r.Version != "0.1.0" || r.RulesReported {
			t.Fatalf("chưa heartbeat: bản lúc đăng ký, không có rule: %+v", r)
		}
	}
	now := time.Now().Truncate(time.Second)
	for i, on := range []int{0, 1} {
		p := app.ProgressPoint{At: now.Add(time.Duration(i) * 5 * time.Minute), Version: "v2", OnTarget: on, Total: 1}
		if err := g.RecordRolloutProgress(ctx, p, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	g.RecordRolloutProgress(ctx, app.ProgressPoint{At: now.Add(-2 * time.Hour), Version: "v2", Total: 1}, time.Hour)
	ps, err := g.RolloutProgress(ctx, "v2", now.Add(-24*time.Hour))
	if err != nil || len(ps) != 2 || ps[1].OnTarget != 1 {
		t.Fatalf("tiến độ (mốc quá hạn phải bị xóa ở lần ghi sau): %v %+v", err, ps)
	}
}

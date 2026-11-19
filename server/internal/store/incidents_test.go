package store

import (
	"context"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/server/internal/periods"
)

// Báo cáo sự cố trên PostgreSQL thật: lấy giai đoạn cảnh báo bắt đầu trong kỳ (không lấy giai đoạn OK), kèm máy,
// siêu thị, rule; cắt theo limit.
func TestIncidentReportOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	id := approvedDevice(t, g, "12351-TN1-PRI", "203.0.113.61")
	heartbeatWith(t, g, id, "203.0.113.61", nil)
	now := time.Now()
	if err := g.AddEvents(ctx, id, []periods.Event{{RuleID: "DISK-IO-ERR-01", Severity: "high", Count: 3, First: now, Last: now, Sample: "I/O error"}}); err != nil {
		t.Fatal(err)
	}
	rows, cut, err := g.IncidentReport(ctx, now.Add(-time.Hour), now.Add(time.Hour), 100)
	if err != nil || cut {
		t.Fatal(err, cut)
	}
	found := false
	for _, r := range rows {
		if r.Kind == "ok" {
			t.Fatal("không được lấy giai đoạn OK")
		}
		if r.DeviceID == id && r.Kind == periods.KindAlert && r.Hostname == "12351-TN1-PRI" && r.StoreCode == "12351" &&
			r.Rules["DISK-IO-ERR-01"] != nil && r.Rules["DISK-IO-ERR-01"].Count == 3 && r.EndedAt.IsZero() {
			found = true
		}
	}
	if !found {
		t.Fatalf("không thấy sự cố: %+v", rows)
	}
	if rows, cut, _ := g.IncidentReport(ctx, now.Add(-time.Hour), now.Add(time.Hour), 0); len(rows) != 0 || !cut {
		t.Fatalf("limit 0 phải cắt: %d %v", len(rows), cut)
	}
}

package store

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/thangkes/SCS-System/server/internal/app"
)

// Thay thế máy cài lại trên PostgreSQL thật: danh sách chờ có gợi ý thay thế + so sánh phần cứng; thay thế thu hồi máy
// cũ, duyệt máy mới (không vướng chỉ mục trùng hostname), chuyển nhóm thử và cấu hình riêng, ghi replaces_device /
// replaced_by; máy không trùng thì bị từ chối.
func TestReplaceOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	oldID := approvedDevice(t, g, "12349-TN1-PRI", "203.0.113.51")
	if _, err := pool.Exec(ctx, `UPDATE devices SET test_ring = true, fp_components = '{"board_serial":"BSN9","primary_mac":"AA:BB:CC:00:00:09"}' WHERE id = $1`, oldID); err != nil {
		t.Fatal(err)
	}
	if err := g.SetConfig(ctx, app.ConfigEntry{Scope: "device", Key: itoa(oldID), WatchServices: []string{"pos.service"}, UpdatedBy: "Quản trị"}); err != nil {
		t.Fatal(err)
	}
	res, err := g.Enroll(ctx, enrollReq(t, "12349-TN1-PRI", "203.0.113.51"))
	if err != nil {
		t.Fatal(err)
	}
	newID := res.DeviceID
	if _, err := pool.Exec(ctx, `UPDATE devices SET fp_components = '{"machine_id":"moi","board_serial":"BSN9","primary_mac":"aa:bb:cc:00:00:09"}' WHERE id = $1`, newID); err != nil {
		t.Fatal(err)
	}
	pend, err := g.ListDevices(ctx, app.DevicePending)
	if err != nil {
		t.Fatal(err)
	}
	var cand *app.ReplaceCandidate
	for _, d := range pend {
		if d.ID == newID {
			cand = d.Replace
		}
	}
	if cand == nil || cand.ID != oldID || cand.Hardware != "same" || len(cand.Same) != 2 {
		t.Fatalf("gợi ý thay thế: %+v", cand)
	}
	if _, err := g.Approve(ctx, newID, "Quản trị", ""); !errors.Is(err, app.ErrConflict) {
		t.Fatalf("duyệt thường phải báo trùng: %v", err)
	}
	other := approvedDevice(t, g, "12350-TN1-PRI", "203.0.113.52")
	if _, err := g.ReplaceApprove(ctx, newID, other, "Quản trị", ""); !errors.Is(err, app.ErrWrongState) {
		t.Fatalf("thay máy không trùng phải bị từ chối: %v", err)
	}
	dev, err := g.ReplaceApprove(ctx, newID, oldID, "Quản trị", "")
	if err != nil {
		t.Fatal(err)
	}
	if dev.ID != newID || !dev.TestRing || dev.ReplacesDevice != oldID {
		t.Fatalf("máy mới: %+v", dev)
	}
	var oldStatus string
	var watch []string
	if err := pool.QueryRow(ctx, `SELECT status FROM devices WHERE id = $1`, oldID).Scan(&oldStatus); err != nil || oldStatus != "revoked" {
		t.Fatalf("máy cũ phải bị thu hồi: %q %v", oldStatus, err)
	}
	if err := pool.QueryRow(ctx, `SELECT watch_services FROM agent_config WHERE scope = 'device' AND scope_key = $1`, itoa(newID)).Scan(&watch); err != nil || len(watch) != 1 {
		t.Fatalf("cấu hình riêng phải chuyển sang: %v %v", watch, err)
	}
	revoked, _ := g.ListDevices(ctx, app.DeviceRevoked)
	for _, d := range revoked {
		if d.ID == oldID && d.ReplacedBy != newID {
			t.Fatalf("máy cũ phải biết bị máy nào thay: %+v", d)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

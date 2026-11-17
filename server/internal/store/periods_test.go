package store

import (
	"context"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/server/internal/app"
	"github.com/thangkes/SCS-System/server/internal/periods"
)

// Lưu theo giai đoạn trên PostgreSQL thật (chạy khi có SCS_TEST_DATABASE_URL, ví dụ setup.sh --test).
func TestPeriodsOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	res, err := g.Enroll(ctx, enrollReq(t, "12345-TN1-PRI", "203.0.113.10"))
	if err != nil {
		t.Fatal(err)
	}
	id := res.DeviceID
	if _, err := g.Approve(ctx, id, "Quản trị", ""); err != nil {
		t.Fatal(err)
	}
	hb := func(sig string) {
		t.Helper()
		if err := g.RecordHeartbeat(ctx, id, app.HeartbeatRecord{IP: netip.MustParseAddr("203.0.113.10"),
			AgentVersion: "0.2.0", RuleSetVersion: "bo@1", UptimeS: 60, AgentTime: time.Now(),
			Status: []byte(`{"os":"Ubuntu"}`), Sig: sig, Summary: map[string]any{"os": "Ubuntu"}}); err != nil {
			t.Fatal(err)
		}
	}
	sigA := "a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0"
	sigB := "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"
	hb(sigA)
	hb(sigA)
	hb(sigB)
	if n := count(`SELECT count(*) FROM device_status WHERE device_id = $1`, id); n != 1 {
		t.Fatalf("device_status phải một dòng, có %d", n)
	}
	if n := count(`SELECT count(*) FROM device_status_changes WHERE device_id = $1`, id); n != 2 {
		t.Fatalf("lịch sử chỉ ghi khi dấu đổi: %d dòng, muốn 2", n)
	}
	if n := count(`SELECT count(*) FROM device_periods WHERE device_id = $1 AND kind = 'ok' AND ended_at IS NULL`, id); n != 1 {
		t.Fatal("phải có một giai đoạn OK đang mở")
	}

	now := time.Now()
	if err := g.AddEvents(ctx, id, []periods.Event{{RuleID: "SVC-01", Severity: "medium", Count: 2, First: now, Last: now, Sample: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEvents(ctx, id, []periods.Event{{RuleID: "RAID-01", Severity: "critical", Count: 1, First: now, Last: now, Sample: "md0 degraded"}}); err != nil {
		t.Fatal(err)
	}
	tl, err := g.ListPeriods(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Periods) != 2 || tl.Periods[0].Kind != periods.KindAlert || tl.Periods[0].Severity != "critical" ||
		tl.Periods[1].Rules["SVC-01"].Count != 2 || tl.Periods[1].EndedAt.IsZero() || len(tl.Changes) != 2 {
		t.Fatalf("dòng thời gian: %+v", tl)
	}
	alerts, err := g.ListAlerts(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var ev app.Alert
	for _, a := range alerts {
		if a.Kind == "event" {
			ev = a
		}
	}
	if ev.PeriodID != tl.Periods[0].ID || ev.Severity != "critical" {
		t.Fatalf("cảnh báo của giai đoạn: %+v", alerts)
	}
	// Critical không tự đóng; kiểm định kỳ sau 2 ngày (máy vẫn gửi) không đổi gì.
	hb(sigB)
	if n, err := g.PeriodTick(ctx, time.Now().Add(-time.Minute)); err != nil || n != 0 {
		t.Fatalf("tick: %d %v", n, err)
	}
	// Admin xác nhận cảnh báo = "Đã xử lý".
	if err := g.AckAlert(ctx, ev.ID, "Quản trị"); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM device_periods WHERE device_id = $1 AND kind = 'alert' AND closed_by = 'admin:Quản trị'`, id); n != 1 {
		t.Fatal("cảnh báo phải được đóng bởi admin")
	}
	if n := count(`SELECT count(*) FROM alerts WHERE kind = 'event' AND acked_at IS NULL`); n != 0 {
		t.Fatal("dòng cảnh báo phải được đóng cùng giai đoạn")
	}
	// Mất liên lạc: đẩy giờ heartbeat cuối về quá khứ rồi kiểm định kỳ.
	if _, err := pool.Exec(ctx, `UPDATE device_status SET received_at = now() - interval '1 hour' WHERE device_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if n, err := g.PeriodTick(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("tick mất liên lạc: %d %v", n, err)
	}
	if n := count(`SELECT count(*) FROM alerts WHERE kind = 'offline' AND acked_at IS NULL AND device_id = $1`, id); n != 1 {
		t.Fatal("phải có cảnh báo mất liên lạc")
	}
	hb(sigB)
	if n := count(`SELECT count(*) FROM alerts WHERE kind = 'offline' AND acked_at IS NULL`); n != 0 {
		t.Fatal("máy gửi lại phải đóng cảnh báo mất liên lạc")
	}
	if n := count(`SELECT count(*) FROM device_periods WHERE device_id = $1 AND kind = 'ok' AND ended_at IS NULL`, id); n != 1 {
		t.Fatal("máy gửi lại phải mở OK")
	}
	// Thu hồi máy: kiểm định kỳ đóng giai đoạn, không mở mới.
	if err := g.Revoke(ctx, id, "Quản trị"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.PeriodTick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM device_periods WHERE device_id = $1 AND ended_at IS NULL`, id); n != 0 {
		t.Fatal("máy thu hồi không còn giai đoạn mở")
	}
}

// Gán bộ rule theo cấp (M7) trên PostgreSQL thật: cấp máy thắng siêu thị thắng chung; bỏ gán không
// làm mất cấu hình khác của cùng cấp.
func TestRuleSetOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	res, err := g.Enroll(ctx, enrollReq(t, "12345-TN1-PRI", "203.0.113.10"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Approve(ctx, res.DeviceID, "Quản trị", ""); err != nil {
		t.Fatal(err)
	}
	dev := strconv.FormatInt(res.DeviceID, 10)
	set := func(scope, key, v string) {
		t.Helper()
		if err := g.SetRuleSet(ctx, app.ConfigEntry{Scope: scope, Key: key, RuleSet: v, UpdatedBy: "Quản trị"}); err != nil {
			t.Fatal(err)
		}
	}
	plan := func() string {
		t.Helper()
		p, err := g.AgentPlan(ctx, res.DeviceID, "amd64")
		if err != nil {
			t.Fatal(err)
		}
		return p.RuleSet
	}
	set("global", "", "bo@1")
	if v := plan(); v != "bo@1" {
		t.Fatalf("chung: %q", v)
	}
	set("store", "12345", "bo@2")
	set("device", dev, "bo@3")
	if v := plan(); v != "bo@3" {
		t.Fatalf("máy phải thắng: %q", v)
	}
	if err := g.SetLabels(ctx, app.ConfigEntry{Scope: "device", Key: dev, Labels: map[string]string{"role": "pri"}, UpdatedBy: "x"}); err != nil {
		t.Fatal(err)
	}
	set("device", dev, "")
	if v := plan(); v != "bo@2" {
		t.Fatalf("bỏ gán cấp máy: %q", v)
	}
	if p, _ := g.AgentPlan(ctx, res.DeviceID, "amd64"); p.Labels["role"] != "pri" {
		t.Fatal("bỏ gán bộ rule không được xóa nhãn máy")
	}
	if err := g.SetRuleSet(ctx, app.ConfigEntry{Scope: "global", RuleSet: "../x@1", UpdatedBy: "x"}); err == nil {
		t.Fatal("DB phải chặn tên bộ rule sai")
	}
}

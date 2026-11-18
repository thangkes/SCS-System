package store

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/server/internal/app"
)

// Duyệt tự động trên PostgreSQL thật: cần SCS_TEST_DATABASE_URL (xem migrate_test.go).
func TestAutoApproveOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	const wan = "203.0.113.10"
	ip := netip.MustParseAddr(wan)

	enroll := func(host, from string) int64 {
		t.Helper()
		res, err := g.Enroll(ctx, enrollReq(t, host, from))
		if err != nil {
			t.Fatal(err)
		}
		return res.DeviceID
	}
	note := func(id int64) string {
		var n *string
		if err := pool.QueryRow(ctx, `SELECT auto_note FROM devices WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == nil {
			return ""
		}
		return *n
	}

	// Mặc định tắt: không kiểm, không ghi lý do.
	if st, err := g.AutoApproveSetting(ctx); err != nil || st.On {
		t.Fatalf("mặc định phải tắt: %+v %v", st, err)
	}
	id := enroll("12345-TN1-PRI", wan)
	if r, err := g.AutoApprove(ctx, id, ip); err != nil || !r.Off || r.Approved || note(id) != "" {
		t.Fatalf("tắt: %+v %v", r, err)
	}

	if err := g.SetAutoApprove(ctx, true, "Quản trị"); err != nil {
		t.Fatal(err)
	}
	// Siêu thị chỉ có dòng trống (do đăng ký tạo ra) → chưa có thông tin.
	if r, err := g.AutoApprove(ctx, id, ip); err != nil || r.Approved || !strings.Contains(note(id), "chưa có thông tin") {
		t.Fatalf("chưa có thông tin: %+v %v %q", r, err, note(id))
	}
	// Nạp thông tin, IP thêm → duyệt.
	if err := g.SaveStores(ctx, []app.StoreInfo{{Code: "12345", Name: "Siêu thị thử", IPs: []string{"192.0.2.1", wan}}}, "Quản trị", "x.csv"); err != nil {
		t.Fatal(err)
	}
	r, err := g.AutoApprove(ctx, id, ip)
	if err != nil || !r.Approved {
		t.Fatalf("đủ điều kiện phải duyệt: %+v %v", r, err)
	}
	var by string
	var nAudit int
	pool.QueryRow(ctx, `SELECT decided_by FROM devices WHERE id = $1 AND status = 'approved' AND auto_note IS NULL`, id).Scan(&by)
	pool.QueryRow(ctx, `SELECT count(*) FROM audit WHERE action = 'device.approve' AND device_id = $1 AND (detail->>'auto')::boolean`, id).Scan(&nAudit)
	if by != app.AutoApprover || nAudit != 1 {
		t.Fatalf("người duyệt %q, nhật ký %d", by, nAudit)
	}

	// Sai IP / trùng hostname (cài lại khóa mới) / hostname sai → chờ kèm lý do.
	for _, c := range []struct{ host, from, want string }{
		{"12345-TN2-SEC", "198.51.100.7", "không thuộc IP WAN"},
		{"12345-TN1-PRI", wan, "Trùng hostname"},
		{"may-le", wan, "Hostname sai"},
	} {
		x := enroll(c.host, c.from)
		r, err := g.AutoApprove(ctx, x, netip.MustParseAddr(c.from))
		if err != nil || r.Approved || !strings.Contains(note(x), c.want) {
			t.Fatalf("%s: %+v %v %q", c.host, r, err, note(x))
		}
		if c.want == "Hostname sai" {
			if list, _ := g.ListDevices(ctx, app.DevicePending); !strings.Contains(list[0].AutoNote+list[len(list)-1].AutoNote, "Hostname sai") {
				t.Fatal("danh sách chờ duyệt phải trả auto_note")
			}
		}
	}

	// Tắt → xóa lý do của máy đang chờ.
	if err := g.SetAutoApprove(ctx, false, "Quản trị"); err != nil {
		t.Fatal(err)
	}
	var left int
	pool.QueryRow(ctx, `SELECT count(*) FROM devices WHERE auto_note IS NOT NULL`).Scan(&left)
	if left != 0 {
		t.Fatalf("còn %d lý do sau khi tắt", left)
	}
}

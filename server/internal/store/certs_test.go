package store

import (
	"context"
	"testing"
	"time"
)

// Kiểm chứng chỉ máy trên PostgreSQL thật: còn dưới 14 ngày → cert_expiring; hết hạn → cert_expired (đóng
// cert_expiring); gia hạn xong → tự đóng; chạy lại không mở trùng.
func TestCertTickOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	id := approvedDevice(t, g, "12348-TN1-PRI", "203.0.113.41")
	now := time.Now().UTC()
	const warn = 14 * 24 * time.Hour
	setExp := func(d time.Duration) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE devices SET cert_expires = $2 WHERE id = $1`, id, now.Add(d)); err != nil {
			t.Fatal(err)
		}
	}
	open := func(kind string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE device_id = $1 AND kind = $2 AND acked_at IS NULL`, id, kind).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tick := func() (int, int) {
		t.Helper()
		o, c, err := g.CertTick(ctx, now, warn)
		if err != nil {
			t.Fatal(err)
		}
		return o, c
	}
	setExp(60 * 24 * time.Hour)
	if o, _ := tick(); o != 0 || open("cert_expiring") != 0 {
		t.Fatal("còn 60 ngày: không cảnh báo")
	}
	setExp(10 * 24 * time.Hour)
	if o, _ := tick(); o != 1 || open("cert_expiring") != 1 {
		t.Fatal("còn 10 ngày chưa gia hạn: phải mở cert_expiring")
	}
	if o, _ := tick(); o != 0 || open("cert_expiring") != 1 {
		t.Fatal("chạy lại không được mở trùng")
	}
	setExp(-time.Hour)
	if o, c := tick(); o != 1 || c != 1 || open("cert_expiring") != 0 || open("cert_expired") != 1 {
		t.Fatalf("hết hạn: phải đổi sang cert_expired (mở %d, đóng %d)", o, c)
	}
	setExp(89 * 24 * time.Hour) // gia hạn xong
	if _, c := tick(); c != 1 || open("cert_expired") != 0 {
		t.Fatal("gia hạn xong phải tự đóng")
	}
}

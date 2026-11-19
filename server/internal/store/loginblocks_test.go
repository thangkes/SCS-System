package store

import (
	"context"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/server/internal/auth"
)

// Khóa đăng nhập trên PostgreSQL thật: lưu, gia hạn giữ hạn dài hơn, nạp bỏ khóa hết hạn, gỡ một / gỡ hết;
// IP siêu thị nhận ra qua IP WAN và qua IP máy đã duyệt.
func TestLoginBlocksOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	if _, err := pool.Exec(ctx, `DELETE FROM login_blocks`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	long := auth.Block{Kind: auth.BlockIP, Key: "203.0.113.5", IP: "203.0.113.5", Reason: "sai mã", Until: now.Add(7 * 24 * time.Hour), Created: now}
	if err := g.SaveBlock(ctx, long); err != nil {
		t.Fatal(err)
	}
	short := long
	short.Until = now.Add(time.Hour)
	if err := g.SaveBlock(ctx, short); err != nil {
		t.Fatal(err)
	}
	dev := auth.Block{Kind: auth.BlockDevice, Key: "abababababababababababababababab", IP: "192.0.2.50", Reason: "sai tên", Until: now.Add(time.Hour), Created: now}
	old := auth.Block{Kind: auth.BlockIP, Key: "198.51.100.1", Reason: "cũ", Until: now.Add(-time.Minute), Created: now.Add(-time.Hour)}
	for _, b := range []auth.Block{dev, old} {
		if err := g.SaveBlock(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	list, err := g.LoadBlocks(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("nạp phải bỏ khóa hết hạn: %+v %v", list, err)
	}
	for _, b := range list {
		if b.Key == long.Key && !b.Until.Equal(long.Until) {
			t.Fatalf("gia hạn phải giữ hạn dài hơn: %v", b.Until)
		}
	}
	if err := g.DeleteBlocks(ctx, auth.BlockDevice, dev.Key, "Quản trị"); err != nil {
		t.Fatal(err)
	}
	if list, _ := g.LoadBlocks(ctx); len(list) != 1 || list[0].Key != long.Key {
		t.Fatalf("gỡ một khóa: %+v", list)
	}
	if err := g.DeleteBlocks(ctx, "", "", "login-code -unlock"); err != nil {
		t.Fatal(err)
	}
	if list, _ := g.LoadBlocks(ctx); len(list) != 0 {
		t.Fatalf("gỡ hết: %+v", list)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit WHERE action IN ('login.block', 'login.unblock')`).Scan(&n); err != nil || n < 5 {
		t.Fatalf("khóa / gỡ phải ghi audit: %d %v", n, err)
	}

	// IP siêu thị.
	if _, err := pool.Exec(ctx, `INSERT INTO stores (code, wan_ip, extra_ips) VALUES ('77001', '192.0.2.10', '{192.0.2.11}')
		ON CONFLICT (code) DO UPDATE SET wan_ip = EXCLUDED.wan_ip, extra_ips = EXCLUDED.extra_ips`); err != nil {
		t.Fatal(err)
	}
	for ip, want := range map[string]bool{"192.0.2.10": true, "192.0.2.11": true, "192.0.2.12": false, "khong-phai-ip": false} {
		if got, err := g.IsSiteIP(ctx, ip); err != nil || got != want {
			t.Errorf("IsSiteIP(%s) = %v %v, phải %v", ip, got, err, want)
		}
	}
}

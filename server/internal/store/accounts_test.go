package store

import (
	"context"
	"testing"

	"github.com/thangkes/SCS-System/server/internal/auth"
)

// Tài khoản trên PostgreSQL thật: cần SCS_TEST_DATABASE_URL (xem migrate_test.go).
// Giới hạn phiên mặc định 24 giờ / 4 phiên; đặt được trong khoảng, ngoài khoảng bị CHECK chặn.
func TestAccountSessionLimitsOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	if err := g.CreateAccount(ctx, auth.Account{Username: "10123", Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	a, err := g.AccountByName(ctx, "10123")
	if err != nil || a.SessionHours != auth.DefaultSessionHours || a.MaxSessions != auth.DefaultMaxSessions {
		t.Fatalf("mặc định: %+v %v", a, err)
	}
	if err := g.SetSessionLimits(ctx, "10123", 360, 2, "Quản trị"); err != nil {
		t.Fatal(err)
	}
	if a, _ := g.AccountByName(ctx, "10123"); a.SessionHours != 360 || a.MaxSessions != 2 {
		t.Fatalf("đã đặt: %+v", a)
	}
	if err := g.SetSessionLimits(ctx, "10123", 361, 2, "Quản trị"); err == nil {
		t.Fatal("quá 15 ngày phải bị DB chặn")
	}
	if err := g.SetSessionLimits(ctx, "khong-co", 24, 4, "Quản trị"); err != auth.ErrNoAccount {
		t.Fatalf("tài khoản không có: %v", err)
	}
	if list, _ := g.ListAccounts(ctx); len(list) != 1 || list[0].MaxSessions != 2 {
		t.Fatalf("danh sách: %+v", list)
	}
}

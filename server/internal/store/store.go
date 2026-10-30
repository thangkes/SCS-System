// Package store kết nối PostgreSQL.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// Open tạo pool kết nối. Pool kết nối lười: server vẫn khởi động được khi DB
// chưa lên, /readyz sẽ báo chưa sẵn sàng cho tới khi DB kết nối được.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// Không đính kèm lỗi gốc: thông báo của nó có thể chứa chuỗi kết nối.
		return nil, errors.New("chuỗi kết nối DB không hợp lệ, kiểm tra SCS_DB_* trong .env")
	}
	cfg.MaxConns = 10
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["application_name"] = "scs-server"
	return pgxpool.NewWithConfig(ctx, cfg)
}

// ReadyCheck dùng cho /readyz: chạy một truy vấn nhỏ để chắc DB trả lời được,
// không chỉ là mở được cổng.
func ReadyCheck(p *pgxpool.Pool) httpx.Check {
	return httpx.Check{
		Name: "postgres",
		Fn: func(ctx context.Context) error {
			var one int
			return p.QueryRow(ctx, "SELECT 1").Scan(&one)
		},
	}
}

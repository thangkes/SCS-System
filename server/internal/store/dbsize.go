package store

import "context"

// DBSize: dung lượng database đang dùng (byte), cho trang Tổng quan.
func (g *Registry) DBSize(ctx context.Context) (int64, error) {
	var n int64
	err := g.pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&n)
	return n, err
}

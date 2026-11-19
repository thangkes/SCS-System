package store

import (
	"context"
	"net/netip"

	"github.com/jackc/pgx/v5"

	"github.com/thangkes/SCS-System/server/internal/auth"
)

// Khóa đăng nhập web quản trị (cài đặt auth.BlockStore) và kiểm IP có thuộc siêu thị không.

var _ auth.BlockStore = (*Registry)(nil)

// LoadBlocks: khóa còn hạn (dọn luôn khóa đã hết hạn).
func (g *Registry) LoadBlocks(ctx context.Context) ([]auth.Block, error) {
	if _, err := g.pool.Exec(ctx, `DELETE FROM login_blocks WHERE until <= now()`); err != nil {
		return nil, err
	}
	rows, err := g.pool.Query(ctx, `SELECT kind, key, ip, reason, until, created_at FROM login_blocks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.Block
	for rows.Next() {
		var b auth.Block
		if err := rows.Scan(&b.Kind, &b.Key, &b.IP, &b.Reason, &b.Until, &b.Created); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SaveBlock thêm khóa hoặc gia hạn (giữ hạn dài hơn), ghi audit.
func (g *Registry) SaveBlock(ctx context.Context, b auth.Block) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO login_blocks (kind, key, ip, reason, until, created_at) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (kind, key) DO UPDATE SET ip = EXCLUDED.ip, reason = EXCLUDED.reason,
				until = greatest(login_blocks.until, EXCLUDED.until), created_at = EXCLUDED.created_at`,
			b.Kind, b.Key, b.IP, truncRunes(b.Reason, 300), b.Until, b.Created); err != nil {
			return err
		}
		return audit(ctx, tx, "system", "login.block", 0, "", map[string]any{
			"kind": b.Kind, "key": b.Key, "ip": b.IP, "reason": b.Reason, "until": b.Until})
	})
}

// DeleteBlocks gỡ một khóa (kind, key) hoặc mọi khóa (kind rỗng), ghi audit.
func (g *Registry) DeleteBlocks(ctx context.Context, kind, key, by string) error {
	if by == "" {
		by = "system"
	}
	return g.tx(ctx, func(tx pgx.Tx) error {
		var err error
		if kind == "" {
			_, err = tx.Exec(ctx, `DELETE FROM login_blocks`)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM login_blocks WHERE kind = $1 AND key = $2`, kind, key)
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, truncRunes(by, 80), "login.unblock", 0, "", map[string]any{"kind": kind, "key": key})
	})
}

// IsSiteIP: IP là IP WAN của một siêu thị (đã nạp thông tin) hoặc IP mà một máy đã duyệt đang gửi về.
func (g *Registry) IsSiteIP(ctx context.Context, ip string) (bool, error) {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false, nil
	}
	var ok bool
	err = g.pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM stores WHERE wan_ip = $1::inet OR $1::inet = ANY(extra_ips)) OR
		EXISTS (SELECT 1 FROM devices WHERE status = 'approved' AND last_ip = $1::inet)`, a.Unmap().String()).Scan(&ok)
	return ok, err
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

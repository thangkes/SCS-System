package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// CertTick: server kiểm chứng chỉ máy (worker gọi định kỳ).
//   - còn dưới warn (14 ngày) mà chưa gia hạn → cảnh báo cert_expiring (high);
//   - đã hết hạn → cert_expired (critical), đóng cert_expiring;
//   - đã gia hạn (hạn mới xa hơn warn), máy bị thu hồi / từ chối → tự đóng.
//
// Trả số cảnh báo vừa mở và vừa đóng.
func (g *Registry) CertTick(ctx context.Context, now time.Time, warn time.Duration) (opened, closed int, err error) {
	err = g.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE alerts a SET acked_at = $1, acked_by = 'hệ thống'
			FROM devices d
			WHERE a.device_id = d.id AND a.acked_at IS NULL AND a.kind IN ('cert_expiring', 'cert_expired')
			  AND (d.status <> 'approved' OR d.cert_expires IS NULL OR d.cert_expires > $2
			       OR (a.kind = 'cert_expiring' AND d.cert_expires <= $1)
			       OR (a.kind = 'cert_expired' AND d.cert_expires > $1))`, now, now.Add(warn))
		if err != nil {
			return err
		}
		closed = int(tag.RowsAffected())
		for _, q := range []struct {
			kind, sev, cond string
		}{
			{"cert_expiring", "high", "d.cert_expires > $1 AND d.cert_expires <= $2"},
			{"cert_expired", "critical", "d.cert_expires <= $1"},
		} {
			tag, err := tx.Exec(ctx, `
				INSERT INTO alerts (device_id, store_code, kind, severity, detail)
				SELECT d.id, d.store_code, '`+q.kind+`', '`+q.sev+`',
				       jsonb_build_object('expires', d.cert_expires, 'agent', coalesce(d.agent_version, ''))
				FROM devices d
				WHERE d.status = 'approved' AND d.cert_expires IS NOT NULL AND `+q.cond+`
				  AND NOT EXISTS (SELECT 1 FROM alerts a WHERE a.device_id = d.id AND a.kind = '`+q.kind+`' AND a.acked_at IS NULL)`,
				now, now.Add(warn))
			if err != nil {
				return err
			}
			opened += int(tag.RowsAffected())
		}
		return nil
	})
	return opened, closed, err
}

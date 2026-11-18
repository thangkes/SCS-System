package store

import (
	"context"
	"time"

	"github.com/thangkes/SCS-System/server/internal/app"
)

var _ app.FleetStore = (*Registry)(nil)

// FleetRows: mọi máy với bản agent, lần cuối gọi về, tình trạng bộ rule (một câu SQL, vài nghìn dòng).
func (g *Registry) FleetRows(ctx context.Context) ([]app.FleetRow, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT d.id, d.status, d.test_ring, coalesce(nullif(st.agent_version, ''), d.agent_version),
		       greatest(d.last_seen, st.received_at),
		       st.rules IS NOT NULL,
		       coalesce((st.rules->>'installed')::boolean, false),
		       coalesce((st.rules->>'scanning')::boolean, false)
		FROM devices d LEFT JOIN device_status st ON st.device_id = d.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.FleetRow
	for rows.Next() {
		var r app.FleetRow
		var seen *time.Time
		if err := rows.Scan(&r.ID, &r.Status, &r.TestRing, &r.Version, &seen, &r.RulesReported,
			&r.RulesInstalled, &r.Scanning); err != nil {
			return nil, err
		}
		if seen != nil {
			r.LastSeen = *seen
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordRolloutProgress ghi một mốc tiến độ và xóa mốc cũ hơn keep.
func (g *Registry) RecordRolloutProgress(ctx context.Context, p app.ProgressPoint, keep time.Duration) error {
	if _, err := g.pool.Exec(ctx, `
		INSERT INTO rollout_progress (at, version, on_target, total) VALUES ($1, $2, $3, $4)
		ON CONFLICT (at) DO UPDATE SET version = $2, on_target = $3, total = $4`,
		p.At, p.Version, p.OnTarget, p.Total); err != nil {
		return err
	}
	_, err := g.pool.Exec(ctx, `DELETE FROM rollout_progress WHERE at < $1`, time.Now().Add(-keep))
	return err
}

// RolloutProgress: các mốc của một bản từ since, cũ trước (tối đa 7 ngày × 288 mốc).
func (g *Registry) RolloutProgress(ctx context.Context, version string, since time.Time) ([]app.ProgressPoint, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT at, version, on_target, total FROM rollout_progress
		WHERE version = $1 AND at >= $2 ORDER BY at LIMIT 2100`, version, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.ProgressPoint{}
	for rows.Next() {
		var p app.ProgressPoint
		if err := rows.Scan(&p.At, &p.Version, &p.OnTarget, &p.Total); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

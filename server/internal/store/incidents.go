package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thangkes/SCS-System/server/internal/app"
	"github.com/thangkes/SCS-System/server/internal/periods"
)

var _ app.IncidentStore = (*Registry)(nil)

// IncidentReport: giai đoạn cảnh báo / mất liên lạc bắt đầu trong [from, to), mới nhất trước, tối đa limit dòng.
func (g *Registry) IncidentReport(ctx context.Context, from, to time.Time, limit int) ([]app.IncidentRow, bool, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT p.id, p.device_id, d.hostname, coalesce(d.store_code, ''), coalesce(s.name, ''), p.kind, coalesce(p.severity, ''),
		       p.started_at, p.ended_at, p.events, p.rules, coalesce(p.closed_by, ''), coalesce(p.note, ''), p.offline_since
		FROM device_periods p JOIN devices d ON d.id = p.device_id LEFT JOIN stores s ON s.code = d.store_code
		WHERE p.kind <> 'ok' AND p.started_at >= $1 AND p.started_at < $2
		ORDER BY p.started_at DESC, p.id DESC LIMIT $3`, from, to, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []app.IncidentRow
	for rows.Next() {
		var r app.IncidentRow
		var ended, offline *time.Time
		var rules []byte
		if err := rows.Scan(&r.ID, &r.DeviceID, &r.Hostname, &r.StoreCode, &r.StoreName, &r.Kind, &r.Severity, &r.StartedAt,
			&ended, &r.Events, &rules, &r.ClosedBy, &r.Note, &offline); err != nil {
			return nil, false, err
		}
		if ended != nil {
			r.EndedAt = *ended
		}
		if offline != nil {
			r.OfflineSince = *offline
		}
		if len(rules) > 2 {
			var m map[string]*periods.RuleSum
			if json.Unmarshal(rules, &m) == nil {
				r.Rules = m
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	cut := len(out) > limit
	if cut {
		out = out[:limit]
	}
	return out, cut, nil
}

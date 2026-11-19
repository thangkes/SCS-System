package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/thangkes/SCS-System/server/internal/app"
	"github.com/thangkes/SCS-System/server/internal/periods"
)

// Lưu theo giai đoạn (docs/thiet-ke-luu-theo-giai-doan.md). Mọi thay đổi của một máy làm trong một
// transaction, khóa giai đoạn đang mở của máy đó (FOR UPDATE) để heartbeat, sự kiện và việc kiểm định kỳ
// không ghi chồng lên nhau.

const periodCols = `id, device_id, kind, started_at, ended_at, severity, events, rules, offline_since,
	last_serious, closed_by, note`

func scanPeriod(row pgx.Row) (*periods.Period, error) {
	var p periods.Period
	var ended, offline, serious *time.Time
	var sev, closed, note *string
	var rules []byte
	if err := row.Scan(&p.ID, &p.DeviceID, &p.Kind, &p.StartedAt, &ended, &sev, &p.Events, &rules, &offline,
		&serious, &closed, &note); err != nil {
		return nil, err
	}
	if ended != nil {
		p.EndedAt = *ended
	}
	if offline != nil {
		p.OfflineSince = *offline
	}
	if serious != nil {
		p.LastSerious = *serious
	}
	if sev != nil {
		p.Severity = *sev
	}
	if closed != nil {
		p.ClosedBy = *closed
	}
	if note != nil {
		p.Note = *note
	}
	p.Rules = map[string]*periods.RuleSum{}
	if len(rules) > 0 {
		if err := json.Unmarshal(rules, &p.Rules); err != nil {
			return nil, fmt.Errorf("tóm tắt giai đoạn %d hỏng: %w", p.ID, err)
		}
	}
	return &p, nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// openPeriod: giai đoạn đang mở của máy (khóa dòng), nil nếu chưa có.
func openPeriod(ctx context.Context, tx pgx.Tx, deviceID int64) (*periods.Period, error) {
	p, err := scanPeriod(tx.QueryRow(ctx, `SELECT `+periodCols+` FROM device_periods
		WHERE device_id = $1 AND ended_at IS NULL FOR UPDATE`, deviceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// lastSeen: lần cuối nghe máy (heartbeat), zero nếu chưa có.
func lastSeen(ctx context.Context, tx pgx.Tx, deviceID int64) (time.Time, error) {
	var t time.Time
	err := tx.QueryRow(ctx, `SELECT received_at FROM device_status WHERE device_id = $1`, deviceID).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return t, err
}

// alertDetail: phần chi tiết của cảnh báo một giai đoạn alert (nhỏ: tối đa 10 rule).
func alertDetail(p *periods.Period) []byte {
	type item struct {
		Rule     string `json:"rule"`
		Severity string `json:"severity"`
		Count    int    `json:"count"`
	}
	items := []item{}
	for id, s := range p.Rules {
		items = append(items, item{id, s.Severity, s.Count})
	}
	// Nặng trước, nhiều lần trước.
	rank := map[string]int{"critical": 5, "high": 4, "medium": 3, "low": 2, "info": 1}
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && (rank[items[j].Severity] > rank[items[j-1].Severity] ||
			rank[items[j].Severity] == rank[items[j-1].Severity] && items[j].Count > items[j-1].Count); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	if len(items) > 10 {
		items = items[:10]
	}
	b, _ := json.Marshal(map[string]any{"period_id": p.ID, "rules": items, "admin_only": p.AdminOnly()})
	return b
}

func ackedBy(closedBy string) string {
	switch {
	case closedBy == periods.ClosedAuto:
		return "tự đóng"
	case strings.HasPrefix(closedBy, "admin:"):
		return strings.TrimPrefix(closedBy, "admin:")
	}
	return "hệ thống"
}

// savePeriod ghi đè các trường của một giai đoạn đã có.
func savePeriod(ctx context.Context, tx pgx.Tx, p *periods.Period) error {
	rules, _ := json.Marshal(p.Rules)
	_, err := tx.Exec(ctx, `UPDATE device_periods SET kind = $2, ended_at = $3, severity = $4, events = $5, rules = $6,
		offline_since = $7, last_serious = $8, closed_by = $9, note = $10 WHERE id = $1`,
		p.ID, p.Kind, nullTime(p.EndedAt), nullStr(p.Severity), p.Events, rules, nullTime(p.OfflineSince),
		nullTime(p.LastSerious), nullStr(p.ClosedBy), nullStr(p.Note))
	return err
}

func insertPeriod(ctx context.Context, tx pgx.Tx, p *periods.Period) error {
	rules, _ := json.Marshal(p.Rules)
	return tx.QueryRow(ctx, `INSERT INTO device_periods (device_id, kind, started_at, ended_at, severity, events, rules,
		offline_since, last_serious, closed_by, note) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		p.DeviceID, p.Kind, p.StartedAt, nullTime(p.EndedAt), nullStr(p.Severity), p.Events, rules,
		nullTime(p.OfflineSince), nullTime(p.LastSerious), nullStr(p.ClosedBy), nullStr(p.Note)).Scan(&p.ID)
}

// openAlert / syncAlert: dòng cảnh báo (kind 'event') gắn với giai đoạn alert.
func openAlert(ctx context.Context, tx pgx.Tx, p *periods.Period) error {
	_, err := tx.Exec(ctx, `INSERT INTO alerts (device_id, store_code, kind, severity, detail, occurrences, period_id,
		created_at, last_at)
		SELECT $1, d.store_code, 'event', $2, $3, greatest($4, 1), $5, $6, $6 FROM devices d WHERE d.id = $1`,
		p.DeviceID, p.Severity, alertDetail(p), p.Events, p.ID, p.StartedAt)
	return err
}

func syncAlert(ctx context.Context, tx pgx.Tx, p *periods.Period) error {
	_, err := tx.Exec(ctx, `UPDATE alerts SET severity = $2, detail = $3, occurrences = greatest($4, 1), last_at = now()
		WHERE period_id = $1 AND acked_at IS NULL`, p.ID, p.Severity, alertDetail(p), p.Events)
	return err
}

// applyPeriod ghi một Result của gói periods. prev = giai đoạn đang mở trước đó (có thể nil).
func applyPeriod(ctx context.Context, tx pgx.Tx, prev *periods.Period, r periods.Result) (*periods.Period, error) {
	if c := r.Closed; c != nil {
		var err error
		if c.ID == 0 {
			err = insertPeriod(ctx, tx, c) // giai đoạn mở và đóng ngay trong cùng một lần
		} else {
			err = savePeriod(ctx, tx, c)
		}
		if err != nil {
			return nil, err
		}
		if c.Kind == periods.KindAlert {
			if _, err := tx.Exec(ctx, `UPDATE alerts SET acked_at = $2, acked_by = $3, detail = $4,
				severity = coalesce($5, severity) WHERE period_id = $1 AND acked_at IS NULL`,
				c.ID, c.EndedAt, ackedBy(c.ClosedBy), alertDetail(c), nullStr(c.Severity)); err != nil {
				return nil, err
			}
		}
	}
	cur := prev
	if r.Closed != nil {
		cur = nil
	}
	if u := r.Updated; u != nil {
		if err := savePeriod(ctx, tx, u); err != nil {
			return nil, err
		}
		if u.Kind == periods.KindAlert {
			var err error
			if prev != nil && prev.Kind != periods.KindAlert {
				err = openAlert(ctx, tx, u) // OK vừa mở (chưa có gì) đổi thành cảnh báo
			} else {
				err = syncAlert(ctx, tx, u)
			}
			if err != nil {
				return nil, err
			}
		}
		cur = u
	}
	if o := r.Opened; o != nil {
		if err := insertPeriod(ctx, tx, o); err != nil {
			return nil, err
		}
		if o.Kind == periods.KindAlert {
			if err := openAlert(ctx, tx, o); err != nil {
				return nil, err
			}
		}
		cur = o
	}
	if r.OfflineAlert != 0 && (cur != nil || prev != nil) {
		dev := prev
		if dev == nil {
			dev = cur
		}
		if err := offlineAlert(ctx, tx, dev.DeviceID, r.OfflineAlert > 0); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

func offlineAlert(ctx context.Context, tx pgx.Tx, deviceID int64, open bool) error {
	if !open {
		_, err := tx.Exec(ctx, `UPDATE alerts SET acked_at = now(), acked_by = 'máy gửi lại'
			WHERE device_id = $1 AND kind = 'offline' AND acked_at IS NULL`, deviceID)
		return err
	}
	var store *string
	if err := tx.QueryRow(ctx, `SELECT store_code FROM devices WHERE id = $1`, deviceID).Scan(&store); err != nil {
		return err
	}
	s := ""
	if store != nil {
		s = *store
	}
	return raise(ctx, tx, deviceID, s, "offline", "medium", map[string]any{})
}

func (g *Registry) RecordHeartbeat(ctx context.Context, id int64, h app.HeartbeatRecord) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE devices SET last_seen = now(), last_ip = $2, agent_version = $3 WHERE id = $1`,
			id, h.IP.String(), h.AgentVersion); err != nil {
			return err
		}
		// Khóa giai đoạn trước, rồi mới ghi trạng thái: cùng thứ tự với PeriodTick.
		cur, err := openPeriod(ctx, tx, id)
		if err != nil {
			return err
		}
		var oldSig *string
		err = tx.QueryRow(ctx, `SELECT sig FROM device_status WHERE device_id = $1 FOR UPDATE`, id).Scan(&oldSig)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Sức khỏe máy chỉ lưu để hiển thị (thẻ Tình trạng máy, cột Sức khỏe), KHÔNG tạo cảnh báo.
		var healthJSON any
		if h.Health != nil {
			healthJSON, _ = json.Marshal(h.Health)
		}
		var status any
		if h.Status != nil {
			status = []byte(h.Status)
		}
		var rulesJSON any
		if h.Rules != nil {
			rulesJSON, _ = json.Marshal(h.Rules)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO device_status (device_id, received_at, agent_time, ip, uptime_s,
			agent_version, rule_set_version, queue_pending, status, sig, rules, health)
			VALUES ($1, now(), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (device_id) DO UPDATE SET received_at = now(), agent_time = EXCLUDED.agent_time,
				ip = EXCLUDED.ip, uptime_s = EXCLUDED.uptime_s, agent_version = EXCLUDED.agent_version,
				rule_set_version = EXCLUDED.rule_set_version, queue_pending = EXCLUDED.queue_pending,
				status = coalesce(EXCLUDED.status, device_status.status), sig = EXCLUDED.sig, rules = EXCLUDED.rules,
				health = coalesce(EXCLUDED.health, device_status.health)`,
			id, h.AgentTime, h.IP.String(), h.UptimeS, h.AgentVersion, h.RuleSetVersion, h.QueuePending,
			status, nullStr(h.Sig), rulesJSON, healthJSON); err != nil {
			return err
		}
		if h.Sig != "" && (oldSig == nil || *oldSig != h.Sig) {
			ch, _ := json.Marshal(h.Summary)
			if _, err := tx.Exec(ctx, `INSERT INTO device_status_changes (device_id, changes) VALUES ($1, $2)`,
				id, ch); err != nil {
				return err
			}
		}
		_, err = applyPeriod(ctx, tx, cur, periods.Seen(cur, id, time.Now()))
		return err
	})
}

func (g *Registry) AddEvents(ctx context.Context, id int64, evs []periods.Event) error {
	if len(evs) == 0 {
		return nil
	}
	return g.tx(ctx, func(tx pgx.Tx) error {
		cur, err := openPeriod(ctx, tx, id)
		if err != nil {
			return err
		}
		now := time.Now()
		if cur, err = applyPeriod(ctx, tx, cur, periods.Seen(cur, id, now)); err != nil {
			return err
		}
		_, err = applyPeriod(ctx, tx, cur, periods.AddEvents(cur, id, evs, now))
		return err
	})
}

// PeriodTick: tìm máy cần đổi (quá hạn không nghe, cảnh báo high hết hạn, máy không còn được duyệt)
// rồi xử lý từng máy trong transaction riêng.
func (g *Registry) PeriodTick(ctx context.Context, now time.Time) (int, error) {
	pol := g.policy()
	rows, err := g.pool.Query(ctx, `
		SELECT p.device_id FROM device_periods p
		JOIN devices d ON d.id = p.device_id
		LEFT JOIN device_status s ON s.device_id = p.device_id
		WHERE p.ended_at IS NULL AND (
			d.status <> 'approved'
			OR (p.kind = 'ok' AND s.received_at < $1)
			OR (p.kind = 'alert' AND p.offline_since IS NULL AND s.received_at < $1)
			OR (p.kind = 'alert' AND p.severity IS DISTINCT FROM 'critical'
				AND coalesce(p.last_serious, p.started_at) <= $2))
		LIMIT 2000`, now.Add(-pol.OfflineAfter), now.Add(-pol.AutoClose))
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, id := range ids {
		err := g.tx(ctx, func(tx pgx.Tx) error {
			cur, err := openPeriod(ctx, tx, id)
			if err != nil || cur == nil {
				return err
			}
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM devices WHERE id = $1`, id).Scan(&status); err != nil {
				return err
			}
			var r periods.Result
			if status != app.DeviceApproved {
				r = periods.Retire(cur, now)
			} else {
				seen, err := lastSeen(ctx, tx, id)
				if err != nil {
					return err
				}
				r = periods.Tick(cur, seen, now, pol)
			}
			if r.Empty() {
				return nil
			}
			changed++
			_, err = applyPeriod(ctx, tx, cur, r)
			return err
		})
		if err != nil {
			return changed, fmt.Errorf("máy %d: %w", id, err)
		}
	}
	return changed, nil
}

func (g *Registry) ListPeriods(ctx context.Context, deviceID int64, limit int) (app.Timeline, error) {
	var tl app.Timeline
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var exists bool
	if err := g.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM devices WHERE id = $1)`, deviceID).Scan(&exists); err != nil {
		return tl, err
	}
	if !exists {
		return tl, app.ErrNotFound
	}
	var seen *time.Time
	var status, health []byte
	if err := g.pool.QueryRow(ctx, `SELECT received_at, status, health FROM device_status WHERE device_id = $1`, deviceID).Scan(&seen, &status, &health); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return tl, err
	}
	tl.Status, tl.Health = status, health
	if seen != nil {
		tl.LastSeen = *seen
	}
	rows, err := g.pool.Query(ctx, `SELECT `+periodCols+` FROM device_periods WHERE device_id = $1
		ORDER BY started_at DESC, id DESC LIMIT $2`, deviceID, limit)
	if err != nil {
		return tl, err
	}
	defer rows.Close()
	tl.Periods = []periods.Period{}
	for rows.Next() {
		p, err := scanPeriod(rows)
		if err != nil {
			return tl, err
		}
		tl.Periods = append(tl.Periods, *p)
	}
	if err := rows.Err(); err != nil {
		return tl, err
	}
	crows, err := g.pool.Query(ctx, `SELECT at, changes FROM device_status_changes WHERE device_id = $1
		ORDER BY at DESC LIMIT 50`, deviceID)
	if err != nil {
		return tl, err
	}
	defer crows.Close()
	tl.Changes = []app.StatusChange{}
	for crows.Next() {
		var c app.StatusChange
		var b []byte
		if err := crows.Scan(&c.At, &b); err != nil {
			return tl, err
		}
		c.Changes = b
		tl.Changes = append(tl.Changes, c)
	}
	return tl, crows.Err()
}

func (g *Registry) ResolvePeriod(ctx context.Context, periodID int64, by, note string) error {
	return g.tx(ctx, func(tx pgx.Tx) error { return resolvePeriod(ctx, tx, g.policy(), periodID, by, note) })
}

func resolvePeriod(ctx context.Context, tx pgx.Tx, pol periods.Policy, periodID int64, by, note string) error {
	var deviceID int64
	err := tx.QueryRow(ctx, `SELECT device_id FROM device_periods WHERE id = $1`, periodID).Scan(&deviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	if err != nil {
		return err
	}
	cur, err := openPeriod(ctx, tx, deviceID)
	if err != nil {
		return err
	}
	if cur == nil || cur.ID != periodID {
		return app.ErrWrongState
	}
	seen, err := lastSeen(ctx, tx, deviceID)
	if err != nil {
		return err
	}
	now := time.Now()
	r, err := periods.Resolve(cur, by, note, seen, now, pol)
	if err != nil {
		return app.ErrWrongState
	}
	if _, err := applyPeriod(ctx, tx, cur, r); err != nil {
		return err
	}
	return audit(ctx, tx, by, "period.resolve", deviceID, "", map[string]any{"period_id": periodID, "note": note})
}

func (g *Registry) Pulse(ctx context.Context, since time.Time) (app.Pulse, error) {
	var p app.Pulse
	var last *time.Time
	err := g.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE acked_at IS NULL),
		count(*) FILTER (WHERE acked_at IS NULL AND severity IN ('high', 'critical')),
		count(*) FILTER (WHERE acked_at IS NULL AND kind = 'offline'),
		(SELECT count(*) FROM devices WHERE status = 'pending'),
		greatest(max(last_at), max(acked_at))
		FROM alerts`).Scan(&p.OpenAlerts, &p.Serious, &p.Offline, &p.Pending, &last)
	if err != nil {
		return p, err
	}
	if last != nil {
		p.LastChange = *last
	}
	p.Changed = []app.Alert{}
	if since.IsZero() || !p.LastChange.After(since) {
		return p, nil
	}
	rows, err := g.pool.Query(ctx, `
		SELECT a.id, coalesce(a.device_id, 0), coalesce(d.hostname, ''), coalesce(a.store_code, d.store_code, ''),
		       a.kind, a.severity, a.created_at, a.last_at, a.occurrences, a.detail, coalesce(a.period_id, 0)
		FROM alerts a LEFT JOIN devices d ON d.id = a.device_id
		WHERE a.acked_at IS NULL AND a.last_at > $1
		ORDER BY a.last_at DESC LIMIT 20`, since)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var a app.Alert
		var detail []byte
		if err := rows.Scan(&a.ID, &a.DeviceID, &a.Hostname, &a.StoreCode, &a.Kind, &a.Severity, &a.CreatedAt,
			&a.LastAt, &a.Occurrences, &detail, &a.PeriodID); err != nil {
			return p, err
		}
		a.Detail = detail
		p.Changed = append(p.Changed, a)
	}
	return p, rows.Err()
}

func (g *Registry) RuleUsage(ctx context.Context) ([]app.RuleUsage, int, int, error) {
	out := []app.RuleUsage{}
	rows, err := g.pool.Query(ctx, `
		SELECT s.rules->>'name', s.rules->>'version', coalesce(s.rules->>'sha256', ''), count(*)
		FROM device_status s JOIN devices d ON d.id = s.device_id AND d.status = 'approved'
		WHERE s.rules IS NOT NULL AND (s.rules->>'installed')::boolean
		GROUP BY 1, 2, 3 ORDER BY 4 DESC LIMIT 200`)
	if err != nil {
		return nil, 0, 0, err
	}
	for rows.Next() {
		var u app.RuleUsage
		if err := rows.Scan(&u.Name, &u.Version, &u.SHA256, &u.Devices); err != nil {
			rows.Close()
			return nil, 0, 0, err
		}
		out = append(out, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	var none, unknown int
	err = g.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE s.rules IS NOT NULL AND NOT coalesce((s.rules->>'installed')::boolean, false)),
		       count(*) FILTER (WHERE s.rules IS NULL)
		FROM devices d LEFT JOIN device_status s ON s.device_id = d.id
		WHERE d.status = 'approved'`).Scan(&none, &unknown)
	return out, none, unknown, err
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/thangkes/SCS-System/common/rules"
	"github.com/thangkes/SCS-System/server/internal/app"
	"github.com/thangkes/SCS-System/server/internal/lan"
)

// Kiểm kê thiết bị LAN (cài đặt app.LANStore). SQL chưa chạy thử trên PostgreSQL thật khi viết.

var _ app.LANStore = (*Registry)(nil)

// LANReport ghi báo cáo của một máy và gộp vào trạng thái siêu thị trong một giao dịch (khóa dòng siêu thị nên
// các máy cùng siêu thị gửi cùng lúc không ghi đè nhau). Máy chưa duyệt / chưa có mã siêu thị thì bỏ qua.
func (g *Registry) LANReport(ctx context.Context, rep *lan.Reporter, m *rules.LAN, now time.Time) (int, error) {
	logged := 0
	err := g.tx(ctx, func(tx pgx.Tx) error {
		var store *string
		err := tx.QueryRow(ctx, `SELECT store_code FROM devices WHERE id = $1 AND status = 'approved'`, rep.DeviceID).Scan(&store)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (store == nil || *store == "")) {
			return nil
		}
		if err != nil {
			return err
		}
		rep.Store = *store
		if _, err := tx.Exec(ctx, `INSERT INTO lan_sites (store_code, data, updated_at) VALUES ($1, '{}', 'epoch')
			ON CONFLICT (store_code) DO NOTHING`, rep.Store); err != nil {
			return err
		}
		site := &lan.Site{Store: rep.Store}
		var data []byte
		if err := tx.QueryRow(ctx, `SELECT data, updated_at FROM lan_sites WHERE store_code = $1 FOR UPDATE`, rep.Store).
			Scan(&data, &site.SavedAt); err != nil {
			return err
		}
		if len(data) > 2 {
			if err := json.Unmarshal(data, site); err != nil {
				site.Targets = nil // dòng hỏng: dựng lại từ đầu
			}
		}
		if site.SavedAt.Unix() <= 0 {
			site.SavedAt = time.Time{}
		}
		prev, err := lanReporter(ctx, tx, rep.DeviceID)
		if err != nil {
			return err
		}
		changes, saveSite, saveRep := lan.Merge(site, rep, prev, m, now)
		if saveRep {
			miss, _ := json.Marshal(rep.Miss)
			if rep.Miss == nil {
				miss = []byte("{}")
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO lan_reporters (device_id, store_code, list_v, received_at, state, lan_ip, n, up, skip, dur_ms, miss)
				VALUES ($1, $2, $3, $4, $5, $6::inet, $7, $8, $9, $10, $11::jsonb)
				ON CONFLICT (device_id) DO UPDATE SET store_code = EXCLUDED.store_code, list_v = EXCLUDED.list_v,
					received_at = EXCLUDED.received_at, state = EXCLUDED.state, lan_ip = EXCLUDED.lan_ip, n = EXCLUDED.n,
					up = EXCLUDED.up, skip = EXCLUDED.skip, dur_ms = EXCLUDED.dur_ms, miss = EXCLUDED.miss`,
				rep.DeviceID, rep.Store, rep.ListV, now, rep.State, nullText(rep.LANIP), rep.N, int64(rep.Up), int64(rep.Skip),
				rep.DurMs, miss); err != nil {
				return err
			}
		}
		if saveSite {
			b, err := json.Marshal(site)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE lan_sites SET data = $2::jsonb, updated_at = $3 WHERE store_code = $1`,
				rep.Store, b, now); err != nil {
				return err
			}
		}
		for _, c := range changes {
			if _, err := tx.Exec(ctx, `
				INSERT INTO lan_changes (store_code, target_key, ip, type, label, from_status, to_status, at, by_device)
				VALUES ($1, $2, $3::inet, $4, $5, $6, $7, $8, $9)`,
				c.Store, c.Key, c.IP, c.Type, nullText(c.Label), c.From, c.To, c.At, nullID(c.By)); err != nil {
				return err
			}
		}
		logged = len(changes)
		return nil
	})
	return logged, err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

const lanReporterCols = `device_id, store_code, list_v, received_at, state, coalesce(host(lan_ip), ''), n, up, skip, dur_ms, miss`

func scanLANReporter(row pgx.Row) (*lan.Reporter, error) {
	var r lan.Reporter
	var up, skip int64
	var miss []byte
	if err := row.Scan(&r.DeviceID, &r.Store, &r.ListV, &r.ReceivedAt, &r.State, &r.LANIP, &r.N, &up, &skip, &r.DurMs, &miss); err != nil {
		return nil, err
	}
	r.Up, r.Skip, r.SavedAt = uint64(up), uint64(skip), r.ReceivedAt
	if len(miss) > 2 {
		_ = json.Unmarshal(miss, &r.Miss)
	}
	return &r, nil
}

func lanReporter(ctx context.Context, tx pgx.Tx, id int64) (*lan.Reporter, error) {
	r, err := scanLANReporter(tx.QueryRow(ctx, `SELECT `+lanReporterCols+` FROM lan_reporters WHERE device_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// LANData: dữ liệu để dựng trang (store = "" → mọi siêu thị). Máy: đã duyệt, có mã siêu thị.
func (g *Registry) LANData(ctx context.Context, store string) (app.LANData, error) {
	out := app.LANData{Sites: map[string]*lan.Site{}, Reporters: map[string][]lan.Reporter{}, Devices: map[string][]lan.Device{}}
	rows, err := g.pool.Query(ctx, `SELECT store_code, data, updated_at FROM lan_sites WHERE $1 = '' OR store_code = $1`, store)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		s := &lan.Site{}
		var data []byte
		if err := rows.Scan(&s.Store, &data, &s.SavedAt); err != nil {
			rows.Close()
			return out, err
		}
		if json.Unmarshal(data, s) == nil {
			out.Sites[s.Store] = s
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows, err = g.pool.Query(ctx, `SELECT `+lanReporterCols+` FROM lan_reporters
		WHERE ($1 = '' OR store_code = $1)
		  AND device_id IN (SELECT id FROM devices WHERE status = 'approved')`, store)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		r, err := scanLANReporter(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Reporters[r.Store] = append(out.Reporters[r.Store], *r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows, err = g.pool.Query(ctx, `
		SELECT id, store_code, hostname, coalesce(tn_no, 0) FROM devices
		WHERE status = 'approved' AND store_code IS NOT NULL AND ($1 = '' OR store_code = $1)`, store)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var d lan.Device
		var sc string
		if err := rows.Scan(&d.ID, &sc, &d.Hostname, &d.TnNo); err != nil {
			return out, err
		}
		out.Devices[sc] = append(out.Devices[sc], d)
	}
	return out, rows.Err()
}

// LANChanges: lịch sử đổi trạng thái của một siêu thị (mới nhất trước).
func (g *Registry) LANChanges(ctx context.Context, store string, limit int) ([]lan.Change, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT store_code, target_key, host(ip), type, coalesce(label, ''), from_status, to_status, at, coalesce(by_device, 0)
		FROM lan_changes WHERE store_code = $1 ORDER BY at DESC, id DESC LIMIT $2`, store, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lan.Change
	for rows.Next() {
		var c lan.Change
		if err := rows.Scan(&c.Store, &c.Key, &c.IP, &c.Type, &c.Label, &c.From, &c.To, &c.At, &c.By); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneLAN xóa lịch sử cũ hơn before (giữ 10 ngày).
func (g *Registry) PruneLAN(ctx context.Context, before time.Time) (int64, error) {
	tag, err := g.pool.Exec(ctx, `DELETE FROM lan_changes WHERE at < $1`, before)
	return tag.RowsAffected(), err
}

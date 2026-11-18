package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/thangkes/SCS-System/server/internal/app"
)

const settingAutoApprove = "auto_approve"

func (g *Registry) AutoApproveSetting(ctx context.Context) (app.AutoApproveSetting, error) {
	return autoApproveSetting(ctx, g.pool)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func autoApproveSetting(ctx context.Context, q querier) (app.AutoApproveSetting, error) {
	var st app.AutoApproveSetting
	var raw []byte
	var by *string
	err := q.QueryRow(ctx, `SELECT value, updated_by, updated_at FROM settings WHERE key = $1`, settingAutoApprove).
		Scan(&raw, &by, &st.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return app.AutoApproveSetting{}, nil // chưa từng bật: tắt
	case err != nil:
		return st, err
	}
	var v struct {
		On bool `json:"on"`
	}
	if json.Unmarshal(raw, &v) == nil {
		st.On = v.On
	}
	if by != nil {
		st.UpdatedBy = *by
	}
	return st, nil
}

func (g *Registry) SetAutoApprove(ctx context.Context, on bool, by string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		v, _ := json.Marshal(map[string]bool{"on": on})
		if _, err := tx.Exec(ctx, `
			INSERT INTO settings (key, value, updated_by) VALUES ($1, $2, $3)
			ON CONFLICT (key) DO UPDATE SET value = $2, updated_by = $3, updated_at = now()`,
			settingAutoApprove, v, by); err != nil {
			return err
		}
		if !on { // tắt: bỏ lý do cũ, tránh hiện lý do của lần kiểm đã cũ
			if _, err := tx.Exec(ctx, `UPDATE devices SET auto_note = NULL WHERE status = 'pending' AND auto_note IS NOT NULL`); err != nil {
				return err
			}
		}
		return audit(ctx, tx, by, "setting.auto_approve", 0, "", map[string]any{"on": on})
	})
}

// AutoApprove kiểm máy đang chờ duyệt theo app.AutoApproveRules (cùng thứ tự); đủ thì duyệt ngay trong
// cùng transaction, chưa đủ thì ghi lý do vào devices.auto_note. Gọi mỗi lần máy hỏi lại (agent hỏi
// mỗi khoảng 5 phút), nên máy xin trước khi nạp thông tin siêu thị sẽ được duyệt ở lần hỏi kế tiếp.
func (g *Registry) AutoApprove(ctx context.Context, id int64, ip netip.Addr) (res app.AutoApproveResult, err error) {
	err = g.tx(ctx, func(tx pgx.Tx) error {
		set, err := autoApproveSetting(ctx, tx)
		if err != nil {
			return err
		}
		if !set.On {
			res.Off = true
			return nil
		}
		var (
			status, hostname, store string
			hostOK, hasInfo, ipOK   bool
			tn                      int
			nIPs                    int
		)
		e := tx.QueryRow(ctx, `
			SELECT d.status, d.hostname, d.hostname_ok, coalesce(d.store_code, ''), coalesce(d.tn_no, 0),
			       coalesce(s.info_updated_by IS NOT NULL, false),
			       coalesce(cardinality(s.extra_ips) + (s.wan_ip IS NOT NULL)::int, 0),
			       coalesce($2::inet = s.wan_ip OR $2::inet = ANY(s.extra_ips), false)
			FROM devices d LEFT JOIN stores s ON s.code = d.store_code
			WHERE d.id = $1 FOR UPDATE OF d`, id, ip.String()).
			Scan(&status, &hostname, &hostOK, &store, &tn, &hasInfo, &nIPs, &ipOK)
		switch {
		case errors.Is(e, pgx.ErrNoRows):
			return app.ErrNotFound
		case e != nil:
			return e
		case status != app.DevicePending:
			return nil
		}
		res.Store = store
		switch {
		case !hostOK || store == "":
			res.Reason = "Hostname sai quy ước: không đọc được mã siêu thị, cần người duyệt"
		case !hasInfo:
			res.Reason = fmt.Sprintf("Siêu thị %s chưa có thông tin trong dữ liệu đã nạp", store)
		case nIPs == 0:
			res.Reason = fmt.Sprintf("Thông tin siêu thị %s chưa có IP WAN", store)
		case !ipOK:
			res.Reason = fmt.Sprintf("Máy gửi từ IP %s, không thuộc IP WAN của siêu thị %s", ip, store)
		}
		if res.Reason == "" {
			var other int64
			var otherHost string
			e := tx.QueryRow(ctx, `
				SELECT id, hostname FROM devices
				WHERE status = 'approved' AND id <> $1 AND (upper(hostname) = upper($2) OR (store_code = $3 AND tn_no = $4))
				LIMIT 1`, id, hostname, store, tn).Scan(&other, &otherHost)
			switch {
			case e == nil:
				res.Reason = fmt.Sprintf("Trùng hostname / vị trí TN với máy đang hoạt động %s (#%s): cần người duyệt "+
					"(thu hồi máy cũ nếu là cài lại)", otherHost, strconv.FormatInt(other, 10))
			case !errors.Is(e, pgx.ErrNoRows):
				return e
			}
		}
		if res.Reason != "" {
			_, err := tx.Exec(ctx, `UPDATE devices SET auto_note = $2 WHERE id = $1 AND auto_note IS DISTINCT FROM $2`,
				id, res.Reason)
			return err
		}
		if _, err := approveTx(ctx, tx, id, app.AutoApprover, "", map[string]any{
			"auto": true, "ip": ip.String(), "at": time.Now().UTC().Format(time.RFC3339)}); err != nil {
			return err
		}
		res.Approved = true
		return nil
	})
	return res, err
}

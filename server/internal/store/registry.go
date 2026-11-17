package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/app"
	"github.com/thangkes/SCS-System/server/internal/periods"
)

// Registry là kho máy trên PostgreSQL (cài đặt app.Registry).
// Mỗi thao tác là một transaction; ràng buộc trong DB là chốt chặn cuối.
type Registry struct {
	pool *pgxpool.Pool
	// Policy: hạn tự đóng cảnh báo high và hạn mất liên lạc (zero = periods.DefaultPolicy).
	Policy periods.Policy
}

func NewRegistry(pool *pgxpool.Pool) *Registry { return &Registry{pool: pool} }

func (g *Registry) policy() periods.Policy {
	p := g.Policy
	if p.AutoClose <= 0 {
		p.AutoClose = periods.DefaultPolicy.AutoClose
	}
	if p.OfflineAfter <= 0 {
		p.OfflineAfter = periods.DefaultPolicy.OfflineAfter
	}
	return p
}

var _ app.Registry = (*Registry)(nil)

func (g *Registry) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, g.pool, fn)
}

func (g *Registry) Enroll(ctx context.Context, r app.EnrollRequest) (res app.EnrollResult, err error) {
	err = g.tx(ctx, func(tx pgx.Tx) error {
		var status string
		e := tx.QueryRow(ctx, `SELECT id, status FROM devices WHERE public_key = $1 FOR UPDATE`, r.PublicKey).
			Scan(&res.DeviceID, &status)
		switch {
		case e == nil:
			res.Status = status
			return g.enrollExisting(ctx, tx, res.DeviceID, status, r)
		case !errors.Is(e, pgx.ErrNoRows):
			return e
		}

		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM devices WHERE status = 'pending'`).Scan(&pending); err != nil {
			return err
		}
		if pending >= app.MaxPendingRequests {
			return app.ErrTooManyPending
		}
		if err := ensureStore(ctx, tx, r); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO devices (hostname, hostname_ok, hostname_error, store_code, tn_no, tn_role,
				public_key, fingerprint, fp_components, os_version, agent_version, enroll_ip, last_seen, last_ip)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now(), $12)
			RETURNING id`,
			r.Hostname, r.HostnameOK, nullStr(r.HostnameError), nullStr(r.StoreCode), nullInt(r.TN), nullStr(r.Role),
			r.PublicKey, r.Fingerprint, r.Components, r.OSVersion, r.AgentVersion, r.IP.String(),
		).Scan(&res.DeviceID); err != nil {
			return err
		}
		res.Status, res.New = app.DevicePending, true

		if err := raise(ctx, tx, res.DeviceID, r.StoreCode, "enroll_request", "medium",
			map[string]any{"hostname": r.Hostname, "ip": r.IP.String(), "key_id": app.KeyID(r.PublicKey)}); err != nil {
			return err
		}
		if !r.HostnameOK {
			if err := raise(ctx, tx, res.DeviceID, "", "hostname_invalid", "medium",
				map[string]any{"hostname": r.Hostname, "error": r.HostnameError}); err != nil {
				return err
			}
		}
		if err := checkWAN(ctx, tx, res.DeviceID, r.StoreCode, r.IP); err != nil {
			return err
		}
		return audit(ctx, tx, "agent", "device.enroll_request", res.DeviceID, r.StoreCode,
			map[string]any{"hostname": r.Hostname, "ip": r.IP.String()})
	})
	return res, err
}

// enrollExisting: máy đã có (cùng khóa) hỏi lại. Đang chờ duyệt thì cập nhật thông tin
// (kỹ thuật viên có thể vừa sửa hostname); trạng thái khác chỉ ghi lần cuối thấy.
func (g *Registry) enrollExisting(ctx context.Context, tx pgx.Tx, id int64, status string, r app.EnrollRequest) error {
	if status != app.DevicePending {
		_, err := tx.Exec(ctx, `UPDATE devices SET last_seen = now(), last_ip = $2 WHERE id = $1`, id, r.IP.String())
		return err
	}
	if err := ensureStore(ctx, tx, r); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE devices SET hostname = $2, hostname_ok = $3, hostname_error = $4, store_code = $5, tn_no = $6,
			tn_role = $7, fingerprint = $8, fp_components = $9, os_version = $10, agent_version = $11,
			last_seen = now(), last_ip = $12
		WHERE id = $1`,
		id, r.Hostname, r.HostnameOK, nullStr(r.HostnameError), nullStr(r.StoreCode), nullInt(r.TN), nullStr(r.Role),
		r.Fingerprint, r.Components, r.OSVersion, r.AgentVersion, r.IP.String())
	return err
}

func ensureStore(ctx context.Context, tx pgx.Tx, r app.EnrollRequest) error {
	if r.StoreCode == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO stores (code) VALUES ($1) ON CONFLICT DO NOTHING`, r.StoreCode)
	return err
}

// checkWAN: siêu thị đã có IP WAN cố định mà máy gửi từ IP khác → cảnh báo trên web.
func checkWAN(ctx context.Context, tx pgx.Tx, deviceID int64, store string, ip netip.Addr) error {
	if store == "" {
		return nil
	}
	var wan *string
	if err := tx.QueryRow(ctx, `SELECT host(wan_ip) FROM stores WHERE code = $1`, store).Scan(&wan); err != nil {
		return err
	}
	if wan == nil || *wan == ip.String() {
		return nil
	}
	return raise(ctx, tx, deviceID, store, "wan_ip_mismatch", "high",
		map[string]any{"expected": *wan, "got": ip.String()})
}

// raise mở cảnh báo; đã có cảnh báo cùng loại đang mở cho máy thì tăng số lần.
func raise(ctx context.Context, tx pgx.Tx, deviceID int64, store, kind, severity string, detail map[string]any) error {
	d, _ := json.Marshal(detail)
	_, err := tx.Exec(ctx, `
		INSERT INTO alerts (device_id, store_code, kind, severity, detail) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (device_id, kind) WHERE acked_at IS NULL AND kind <> 'event'
		DO UPDATE SET last_at = now(), occurrences = alerts.occurrences + 1, detail = EXCLUDED.detail`,
		deviceID, nullStr(store), kind, severity, d)
	return err
}

func audit(ctx context.Context, tx pgx.Tx, actor, action string, deviceID int64, store string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	d, _ := json.Marshal(detail)
	_, err := tx.Exec(ctx, `INSERT INTO audit (actor, action, device_id, store_code, detail) VALUES ($1, $2, $3, $4, $5)`,
		actor, action, nullInt64(deviceID), nullStr(store), d)
	return err
}

func (g *Registry) SetCertSerial(ctx context.Context, id int64, serial string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE devices SET cert_serial = $2 WHERE id = $1 AND status = 'approved'`, id, serial)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return app.ErrWrongState
		}
		return audit(ctx, tx, "system", "device.cert_issued", id, "", map[string]any{"serial": serial})
	})
}

func (g *Registry) ListDevices(ctx context.Context, status string) ([]app.Device, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT d.id, d.hostname, d.hostname_ok, coalesce(d.hostname_error, ''), coalesce(d.store_code, ''),
		       coalesce(d.tn_no, 0), coalesce(d.tn_role, ''), d.status, d.public_key, d.fingerprint,
		       d.os_version, d.agent_version, host(d.enroll_ip), coalesce(host(s.wan_ip), ''),
		       d.requested_at, d.decided_at, coalesce(d.decided_by, ''), d.last_seen, coalesce(host(d.last_ip), ''), d.test_ring,
		       st.rules
		FROM devices d LEFT JOIN stores s ON s.code = d.store_code
		LEFT JOIN device_status st ON st.device_id = d.id
		WHERE d.status = $1
		ORDER BY d.requested_at DESC
		LIMIT 1000`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.Device
	for rows.Next() {
		var dv app.Device
		var pub []byte
		var decided, seen *time.Time
		var rules []byte
		if err := rows.Scan(&dv.ID, &dv.Hostname, &dv.HostnameOK, &dv.HostnameError, &dv.StoreCode, &dv.TN, &dv.Role,
			&dv.Status, &pub, &dv.Fingerprint, &dv.OSVersion, &dv.AgentVersion, &dv.EnrollIP, &dv.StoreWANIP,
			&dv.RequestedAt, &decided, &dv.DecidedBy, &seen, &dv.LastIP, &dv.TestRing, &rules); err != nil {
			return nil, err
		}
		if len(rules) > 0 {
			var r protocol.RuleSetReport
			if json.Unmarshal(rules, &r) == nil {
				dv.Rules = &r
			}
		}
		dv.KeyID = app.KeyID(pub)
		if decided != nil {
			dv.DecidedAt = *decided
		}
		if seen != nil {
			dv.LastSeen = *seen
		}
		out = append(out, dv)
	}
	return out, rows.Err()
}

func (g *Registry) Approve(ctx context.Context, id int64, by, storeOverride string) (dev app.Device, err error) {
	err = g.tx(ctx, func(tx pgx.Tx) error {
		var status, store, enrollIP string
		e := tx.QueryRow(ctx, `SELECT status, coalesce(store_code, ''), host(enroll_ip) FROM devices WHERE id = $1 FOR UPDATE`, id).
			Scan(&status, &store, &enrollIP)
		switch {
		case errors.Is(e, pgx.ErrNoRows):
			return app.ErrNotFound
		case e != nil:
			return e
		case status != app.DevicePending:
			return app.ErrWrongState
		}
		if store == "" {
			store = storeOverride
		}
		if store == "" {
			return app.ErrNeedStore
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stores (code) VALUES ($1) ON CONFLICT DO NOTHING`, store); err != nil {
			return err
		}
		// Máy đầu tiên được duyệt của siêu thị đặt IP WAN cố định.
		if _, err := tx.Exec(ctx, `UPDATE stores SET wan_ip = $2, updated_at = now() WHERE code = $1 AND wan_ip IS NULL`,
			store, enrollIP); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE devices SET status = 'approved', store_code = $2, decided_at = now(), decided_by = $3 WHERE id = $1`,
			id, store, by); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" { // unique_violation: trùng hostname / vị trí TN
				return fmt.Errorf("%w (%s)", app.ErrConflict, pe.ConstraintName)
			}
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE alerts SET acked_at = now(), acked_by = $2
			WHERE device_id = $1 AND kind = 'enroll_request' AND acked_at IS NULL`, id, by); err != nil {
			return err
		}
		return audit(ctx, tx, by, "device.approve", id, store, nil)
	})
	if err != nil {
		return app.Device{}, err
	}
	list, err := g.ListDevices(ctx, app.DeviceApproved)
	if err != nil {
		return app.Device{}, err
	}
	for _, d := range list {
		if d.ID == id {
			return d, nil
		}
	}
	return app.Device{}, app.ErrNotFound
}

func (g *Registry) setStatus(ctx context.Context, id int64, by, from, to, action string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		var status string
		e := tx.QueryRow(ctx, `SELECT status FROM devices WHERE id = $1 FOR UPDATE`, id).Scan(&status)
		switch {
		case errors.Is(e, pgx.ErrNoRows):
			return app.ErrNotFound
		case e != nil:
			return e
		case status != from:
			return app.ErrWrongState
		}
		if _, err := tx.Exec(ctx, `UPDATE devices SET status = $2, decided_at = now(), decided_by = $3 WHERE id = $1`,
			id, to, by); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE alerts SET acked_at = now(), acked_by = $2
			WHERE device_id = $1 AND kind = 'enroll_request' AND acked_at IS NULL`, id, by); err != nil {
			return err
		}
		return audit(ctx, tx, by, action, id, "", nil)
	})
}

func (g *Registry) Reject(ctx context.Context, id int64, by string) error {
	return g.setStatus(ctx, id, by, app.DevicePending, app.DeviceRejected, "device.reject")
}

func (g *Registry) Revoke(ctx context.Context, id int64, by string) error {
	return g.setStatus(ctx, id, by, app.DeviceApproved, app.DeviceRevoked, "device.revoke")
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func (g *Registry) ListAlerts(ctx context.Context, includeAcked bool) ([]app.Alert, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT a.id, coalesce(a.device_id, 0), coalesce(d.hostname, ''), coalesce(a.store_code, d.store_code, ''),
		       a.kind, a.severity, a.created_at, a.last_at, a.occurrences, a.detail, a.acked_at, coalesce(a.acked_by, ''),
		       coalesce(a.period_id, 0)
		FROM alerts a LEFT JOIN devices d ON d.id = a.device_id
		WHERE $1 OR a.acked_at IS NULL
		ORDER BY a.last_at DESC
		LIMIT 500`, includeAcked)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.Alert
	for rows.Next() {
		var a app.Alert
		var acked *time.Time
		var detail []byte
		if err := rows.Scan(&a.ID, &a.DeviceID, &a.Hostname, &a.StoreCode, &a.Kind, &a.Severity, &a.CreatedAt,
			&a.LastAt, &a.Occurrences, &detail, &acked, &a.AckedBy, &a.PeriodID); err != nil {
			return nil, err
		}
		a.Detail = detail
		if acked != nil {
			a.AckedAt = *acked
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (g *Registry) AckAlert(ctx context.Context, id int64, by string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		var acked *time.Time
		var deviceID, periodID *int64
		var kind string
		e := tx.QueryRow(ctx, `SELECT acked_at, device_id, kind, period_id FROM alerts WHERE id = $1 FOR UPDATE`, id).
			Scan(&acked, &deviceID, &kind, &periodID)
		switch {
		case errors.Is(e, pgx.ErrNoRows):
			return app.ErrNotFound
		case e != nil:
			return e
		case acked != nil:
			return app.ErrWrongState
		}
		// Cảnh báo của một giai đoạn alert đang mở: xác nhận = "Đã xử lý" (đóng giai đoạn, mở OK).
		if kind == "event" && periodID != nil {
			err := resolvePeriod(ctx, tx, g.policy(), *periodID, by, "")
			if !errors.Is(err, app.ErrWrongState) {
				return err
			}
			// Giai đoạn đã đóng (dữ liệu cũ): chỉ xác nhận cảnh báo như bình thường.
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET acked_at = now(), acked_by = $2 WHERE id = $1`, id, by); err != nil {
			return err
		}
		var dev int64
		if deviceID != nil {
			dev = *deviceID
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit (actor, action, device_id, detail) VALUES ($1, 'alert.ack', $2, $3)`,
			by, nullInt64(dev), fmt.Sprintf(`{"alert_id":%d}`, id))
		return err
	})
}

func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func (g *Registry) AgentAuth(ctx context.Context, id int64) (app.AgentAuth, error) {
	var a app.AgentAuth
	var serial *string
	err := g.pool.QueryRow(ctx, `SELECT public_key, status, cert_serial FROM devices WHERE id = $1`, id).
		Scan(&a.PublicKey, &a.Status, &serial)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, app.ErrNotFound
	}
	if serial != nil {
		a.CertSerial = *serial
	}
	return a, err
}

// ---------------------------------------------------------------- bản agent, phát hành, cấu hình

func (g *Registry) AddRelease(ctx context.Context, r app.Release) error {
	_, err := g.pool.Exec(ctx, `
		INSERT INTO agent_releases (version, arch, sha256, size, sig) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (version, arch) DO NOTHING`, r.Version, r.Arch, r.SHA256, r.Size, r.Sig)
	return err
}

func (g *Registry) ListReleases(ctx context.Context) ([]app.Release, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT version, arch, sha256, size, sig, created_at FROM agent_releases
		ORDER BY created_at DESC, version DESC, arch LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.Release
	for rows.Next() {
		var r app.Release
		if err := rows.Scan(&r.Version, &r.Arch, &r.SHA256, &r.Size, &r.Sig, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (g *Registry) GetRelease(ctx context.Context, version, arch string) (app.Release, error) {
	var r app.Release
	err := g.pool.QueryRow(ctx, `
		SELECT version, arch, sha256, size, sig, created_at FROM agent_releases WHERE version = $1 AND arch = $2`,
		version, arch).Scan(&r.Version, &r.Arch, &r.SHA256, &r.Size, &r.Sig, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, app.ErrNotFound
	}
	return r, err
}

func (g *Registry) GetRollout(ctx context.Context) (app.Rollout, error) {
	var ro app.Rollout
	rows, err := g.pool.Query(ctx, `SELECT ring, coalesce(version, '') FROM agent_rollout`)
	if err != nil {
		return ro, err
	}
	defer rows.Close()
	for rows.Next() {
		var ring, v string
		if err := rows.Scan(&ring, &v); err != nil {
			return ro, err
		}
		if ring == app.RingTest {
			ro.Test = v
		} else {
			ro.All = v
		}
	}
	return ro, rows.Err()
}

func (g *Registry) SetRollout(ctx context.Context, ring, version, by string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		if version != "" {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_releases WHERE version = $1`, version).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return app.ErrNotFound
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE agent_rollout SET version = $2, updated_at = now(), updated_by = $3 WHERE ring = $1`,
			ring, nullStr(version), by)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return app.ErrNotFound
		}
		return audit(ctx, tx, by, "agent.rollout", 0, "", map[string]any{"ring": ring, "version": version})
	})
}

func (g *Registry) SetTestRing(ctx context.Context, id int64, on bool, by string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE devices SET test_ring = $2 WHERE id = $1`, id, on)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return app.ErrNotFound
		}
		return audit(ctx, tx, by, "device.test_ring", id, "", map[string]any{"on": on})
	})
}

func (g *Registry) AgentPlan(ctx context.Context, id int64, arch string) (app.AgentPlan, error) {
	var plan app.AgentPlan
	var store *string
	var testRing bool
	err := g.pool.QueryRow(ctx, `SELECT store_code, test_ring FROM devices WHERE id = $1`, id).Scan(&store, &testRing)
	if errors.Is(err, pgx.ErrNoRows) {
		return plan, app.ErrNotFound
	}
	if err != nil {
		return plan, err
	}
	ro, err := g.GetRollout(ctx)
	if err != nil {
		return plan, err
	}
	target := ro.All
	if testRing && ro.Test != "" {
		target = ro.Test
	}
	if target != "" {
		r, err := g.GetRelease(ctx, target, arch)
		switch {
		case err == nil:
			plan.Release = &r
		case !errors.Is(err, app.ErrNotFound):
			return plan, err
		}
	}
	// Cấu hình: lấy cấp cụ thể nhất có giá trị (máy > siêu thị > chung).
	storeKey := ""
	if store != nil {
		storeKey = *store
	}
	err = g.pool.QueryRow(ctx, `
		SELECT watch_services FROM agent_config
		WHERE watch_services IS NOT NULL AND (
			(scope = 'device' AND scope_key = $1) OR (scope = 'store' AND scope_key = $2) OR scope = 'global')
		ORDER BY CASE scope WHEN 'device' THEN 0 WHEN 'store' THEN 1 ELSE 2 END
		LIMIT 1`, fmt.Sprint(id), storeKey).Scan(&plan.WatchServices)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return plan, err
	}
	// Nhãn máy: gộp theo từng khóa, cấp cụ thể hơn ghi đè (chung < siêu thị < máy).
	rows, err := g.pool.Query(ctx, `
		SELECT labels FROM agent_config
		WHERE labels IS NOT NULL AND (
			(scope = 'device' AND scope_key = $1) OR (scope = 'store' AND scope_key = $2) OR scope = 'global')
		ORDER BY CASE scope WHEN 'global' THEN 0 WHEN 'store' THEN 1 ELSE 2 END`, fmt.Sprint(id), storeKey)
	if err != nil {
		return plan, err
	}
	defer rows.Close()
	for rows.Next() {
		var m map[string]string
		if err := rows.Scan(&m); err != nil {
			return plan, err
		}
		for k, v := range m {
			if plan.Labels == nil {
				plan.Labels = map[string]string{}
			}
			plan.Labels[k] = v
		}
	}
	return plan, rows.Err()
}

func (g *Registry) ListConfigs(ctx context.Context) ([]app.ConfigEntry, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT scope, scope_key, watch_services, labels, updated_at, updated_by FROM agent_config
		ORDER BY CASE scope WHEN 'global' THEN 0 WHEN 'store' THEN 1 ELSE 2 END, scope_key LIMIT 5000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.ConfigEntry
	for rows.Next() {
		var c app.ConfigEntry
		if err := rows.Scan(&c.Scope, &c.Key, &c.WatchServices, &c.Labels, &c.UpdatedAt, &c.UpdatedBy); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (g *Registry) SetConfig(ctx context.Context, c app.ConfigEntry) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		var err error
		if c.WatchServices == nil {
			// Bỏ dịch vụ theo dõi ở cấp này; dòng không còn nhãn thì xóa hẳn.
			_, err = tx.Exec(ctx, `UPDATE agent_config SET watch_services = NULL, updated_at = now(), updated_by = $3
				WHERE scope = $1 AND scope_key = $2`, c.Scope, c.Key, c.UpdatedBy)
			if err == nil {
				_, err = tx.Exec(ctx, `DELETE FROM agent_config WHERE scope = $1 AND scope_key = $2
					AND watch_services IS NULL AND labels IS NULL`, c.Scope, c.Key)
			}
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO agent_config (scope, scope_key, watch_services, updated_by) VALUES ($1, $2, $3, $4)
				ON CONFLICT (scope, scope_key) DO UPDATE SET watch_services = EXCLUDED.watch_services,
					updated_at = now(), updated_by = EXCLUDED.updated_by`, c.Scope, c.Key, c.WatchServices, c.UpdatedBy)
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, c.UpdatedBy, "agent.config", 0, "",
			map[string]any{"scope": c.Scope, "key": c.Key, "watch_services": c.WatchServices})
	})
}

func (g *Registry) SetLabels(ctx context.Context, c app.ConfigEntry) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		var err error
		if c.Labels == nil {
			_, err = tx.Exec(ctx, `UPDATE agent_config SET labels = NULL, updated_at = now(), updated_by = $3
				WHERE scope = $1 AND scope_key = $2`, c.Scope, c.Key, c.UpdatedBy)
			if err == nil {
				_, err = tx.Exec(ctx, `DELETE FROM agent_config WHERE scope = $1 AND scope_key = $2
					AND watch_services IS NULL AND labels IS NULL`, c.Scope, c.Key)
			}
		} else {
			raw, merr := json.Marshal(c.Labels)
			if merr != nil {
				return merr
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO agent_config (scope, scope_key, labels, updated_by) VALUES ($1, $2, $3::jsonb, $4)
				ON CONFLICT (scope, scope_key) DO UPDATE SET labels = EXCLUDED.labels,
					updated_at = now(), updated_by = EXCLUDED.updated_by`, c.Scope, c.Key, string(raw), c.UpdatedBy)
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, c.UpdatedBy, "agent.labels", 0, "",
			map[string]any{"scope": c.Scope, "key": c.Key, "labels": c.Labels})
	})
}

package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/thangkes/SCS-System/server/internal/app"
)

// Thông tin siêu thị (cài đặt app.StoreDirectory).

var _ app.StoreDirectory = (*Registry)(nil)

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (g *Registry) ListStores(ctx context.Context) ([]app.StoreInfo, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT code, coalesce(name, ''), coalesce(organization, ''), host(wan_ip),
		       coalesce((SELECT array_agg(host(x)) FROM unnest(extra_ips) x), '{}'),
		       coalesce(manager, ''), coalesce(deputy, ''), coalesce(leader, ''), coalesce(deputy_department, ''),
		       coalesce(note, ''), updated_at, coalesce(info_updated_by, '')
		FROM stores ORDER BY code LIMIT 20000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.StoreInfo
	for rows.Next() {
		var s app.StoreInfo
		var wan *string
		var extra []string
		var upd time.Time
		if err := rows.Scan(&s.Code, &s.Name, &s.Organization, &wan, &extra, &s.Manager, &s.Deputy, &s.Leader,
			&s.DeputyDepartment, &s.Note, &upd, &s.UpdatedBy); err != nil {
			return nil, err
		}
		s.IPs = []string{}
		if wan != nil {
			s.IPs = append(s.IPs, *wan)
		}
		s.IPs = append(s.IPs, extra...)
		s.UpdatedAt = upd
		out = append(out, s)
	}
	return out, rows.Err()
}

func (g *Registry) SaveStore(ctx context.Context, s app.StoreInfo, by string) error {
	var wan any
	extra := []string{}
	if len(s.IPs) > 0 {
		wan, extra = s.IPs[0], s.IPs[1:]
	}
	return g.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO stores (code, name, organization, wan_ip, extra_ips, manager, deputy, leader, deputy_department,
			                    note, info_updated_by)
			VALUES ($1, $2, $3, $4::inet, $5::inet[], $6, $7, $8, $9, $10, $11)
			ON CONFLICT (code) DO UPDATE SET name = $2, organization = $3, wan_ip = $4::inet, extra_ips = $5::inet[],
			  manager = $6, deputy = $7, leader = $8, deputy_department = $9, note = $10, info_updated_by = $11,
			  updated_at = now()`,
			s.Code, nullText(s.Name), nullText(s.Organization), wan, extra, nullText(s.Manager), nullText(s.Deputy),
			nullText(s.Leader), nullText(s.DeputyDepartment), nullText(s.Note), by); err != nil {
			return err
		}
		return audit(ctx, tx, by, "store.update", 0, s.Code, map[string]any{
			"name": s.Name, "ips": s.IPs, "manager": s.Manager})
	})
}

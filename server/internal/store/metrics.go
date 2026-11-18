package store

import (
	"context"
	"time"

	"github.com/thangkes/SCS-System/server/internal/app"
	"github.com/thangkes/SCS-System/server/internal/sysstat"
)

// Lịch sử thông số server (cài đặt app.MetricsStore).

var _ app.MetricsStore = (*Registry)(nil)

func nullF(v float64) any {
	if v < 0 {
		return nil
	}
	return v
}

func nullU(v uint64) any {
	if v == 0 {
		return nil
	}
	return int64(v)
}

func (g *Registry) InsertServerMetric(ctx context.Context, m sysstat.Minute, dbSize int64) error {
	var db any
	if dbSize >= 0 {
		db = dbSize
	}
	_, err := g.pool.Exec(ctx, `
		INSERT INTO server_metrics (at, host, samples, cpu_avg, cpu_max, load1, mem_used, mem_total, disk_used, disk_total,
		  rx_bytes, tx_bytes, rx_peak_bps, tx_peak_bps, heartbeats, log_events, conns_max, rss, db_size)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		ON CONFLICT (at) DO NOTHING`,
		m.At, nullText(m.Host), min(m.Samples, 32767), nullF(m.CPUAvg), nullF(m.CPUMax), m.Load1,
		nullU(m.MemUsed), nullU(m.MemTotal), nullU(m.DiskUsed), nullU(m.DiskTotal),
		int64(m.RxBytes), int64(m.TxBytes), m.RxPeak, m.TxPeak, m.Heartbeats, m.LogEvents, m.ConnsMax, nullU(m.RSS), db)
	return err
}

func (g *Registry) PruneServerMetrics(ctx context.Context, before time.Time) (int64, error) {
	tag, err := g.pool.Exec(ctx, `DELETE FROM server_metrics WHERE at < $1`, before)
	return tag.RowsAffected(), err
}

// ServerReport gom theo khoảng step giây, tính theo giờ địa phương (offset giây so với UTC) để "ngày" bắt đầu 0 giờ.
func (g *Registry) ServerReport(ctx context.Context, from, to time.Time, stepS, offsetS int) ([]app.MetricRow, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT to_timestamp(floor((extract(epoch FROM at) + $3) / $4) * $4 - $3) AS b,
		       count(*)::int,
		       avg(cpu_avg), max(cpu_max), avg(load1),
		       avg(100.0 * mem_used / nullif(mem_total, 0)), max(100.0 * mem_used / nullif(mem_total, 0)),
		       coalesce(max(mem_total), 0),
		       coalesce((array_agg(disk_used ORDER BY at DESC) FILTER (WHERE disk_used IS NOT NULL))[1], 0),
		       coalesce(max(disk_total), 0),
		       coalesce(sum(rx_bytes), 0)::bigint, coalesce(sum(tx_bytes), 0)::bigint,
		       coalesce(max(rx_peak_bps), 0), coalesce(max(tx_peak_bps), 0),
		       coalesce(sum(heartbeats), 0)::bigint, coalesce(sum(log_events), 0)::bigint, coalesce(max(conns_max), 0)::bigint,
		       coalesce(max(rss), 0),
		       coalesce((array_agg(db_size ORDER BY at DESC) FILTER (WHERE db_size IS NOT NULL))[1], 0)
		FROM server_metrics
		WHERE at >= $1 AND at < $2
		GROUP BY b ORDER BY b`, from, to, offsetS, stepS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.MetricRow
	for rows.Next() {
		var r app.MetricRow
		var rxPeak, txPeak float64
		if err := rows.Scan(&r.At, &r.Minutes, &r.CPUAvg, &r.CPUMax, &r.Load1, &r.MemPctAvg, &r.MemPctMax, &r.MemTotal,
			&r.DiskUsed, &r.DiskTotal, &r.RxBytes, &r.TxBytes, &rxPeak, &txPeak, &r.Heartbeat, &r.LogEvents,
			&r.ConnsMax, &r.RSSMax, &r.DBSize); err != nil {
			return nil, err
		}
		r.RxPeak, r.TxPeak = rxPeak, txPeak
		out = append(out, r)
	}
	return out, rows.Err()
}

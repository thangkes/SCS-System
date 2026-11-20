package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/sysstat"
	"github.com/thangkes/SCS-System/server/internal/tabular"
)

// Lịch sử thông số server và báo cáo (trang "Báo cáo server"):
//   - MetricsRecorder ghi mỗi phút một dòng (CPU, RAM, ổ đĩa, mạng, heartbeat, dung lượng DB) và xóa dòng quá hạn;
//   - GET /admin/v1/server/report gom theo phút / 10 phút / giờ / ngày (giờ của máy server), kèm tổng kết;
//   - GET /admin/v1/server/report.csv xuất cùng số liệu ra CSV (mở bằng Excel).

// MetricRow: một dòng đã gom (một khoảng thời gian).
type MetricRow struct {
	At        time.Time `json:"at"`
	Minutes   int       `json:"minutes"` // số phút có số liệu (ít hơn độ dài khoảng = server tắt / mất số liệu)
	CPUAvg    *float64  `json:"cpu_avg"`
	CPUMax    *float64  `json:"cpu_max"`
	Load1     *float64  `json:"load1"`
	MemPctAvg *float64  `json:"mem_pct_avg"`
	MemPctMax *float64  `json:"mem_pct_max"`
	MemTotal  int64     `json:"mem_total"`
	DiskUsed  int64     `json:"disk_used"` // cuối khoảng
	DiskTotal int64     `json:"disk_total"`
	RxBytes   int64     `json:"rx_bytes"`
	TxBytes   int64     `json:"tx_bytes"`
	RxPeak    float64   `json:"rx_peak_bps"`
	TxPeak    float64   `json:"tx_peak_bps"`
	Heartbeat int64     `json:"heartbeats"`
	LogEvents int64     `json:"log_events"`
	ConnsMax  int64     `json:"conns_max"`
	RSSMax    int64     `json:"rss_max"`
	DBSize    int64     `json:"db_size"` // cuối khoảng
}

// MetricsStore: nơi lưu lịch sử (PostgreSQL).
type MetricsStore interface {
	InsertServerMetric(ctx context.Context, m sysstat.Minute, dbSize int64) error
	PruneServerMetrics(ctx context.Context, before time.Time) (int64, error)
	ServerReport(ctx context.Context, from, to time.Time, stepS, offsetS int) ([]MetricRow, error)
}

// MetricsRecorder: chạy cùng tiến trình web quản trị (cần Stats); ghi đầu mỗi phút.
func MetricsRecorder(d Deps, keep time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		ms, ok := d.Registry.(MetricsStore)
		if d.Stats == nil || !ok {
			<-ctx.Done()
			return nil
		}
		sz, _ := d.Registry.(dbSizer)
		var dbSize int64 = -1
		var dbAt, prunedAt time.Time
		d.Stats.TakeMinute(time.Now()) // bỏ phần lẻ trước phút đầu tiên
		for {
			now := time.Now()
			next := now.Truncate(time.Minute).Add(time.Minute)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(next.Sub(now)):
			}
			m := d.Stats.TakeMinute(next.Add(-time.Minute)) // ghi theo đầu phút vừa qua
			if m.Samples == 0 {
				continue
			}
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			if sz != nil && time.Since(dbAt) >= 10*time.Minute {
				if n, err := sz.DBSize(c); err == nil {
					dbSize = n
				}
				dbAt = time.Now()
			}
			if err := ms.InsertServerMetric(c, m, dbSize); err != nil {
				d.Log.Warn("không ghi được lịch sử thông số server", "err", err)
			}
			if time.Since(prunedAt) >= time.Hour {
				if n, err := ms.PruneServerMetrics(c, time.Now().Add(-keep)); err == nil && n > 0 {
					d.Log.Info("xóa lịch sử thông số server quá hạn", "dong", n)
				}
				prunedAt = time.Now()
			}
			cancel()
		}
	}
}

// ---------------------------------------------------------------- báo cáo

// reportSummary: tổng kết cả khoảng.
type reportSummary struct {
	Minutes                            int
	CPUAvg, CPUMax, MemAvg, MemMax     *float64
	DiskStart, DiskEnd, DiskTotal      int64
	RxBytes, TxBytes                   int64
	RxAvgBps, TxAvgBps, RxPeak, TxPeak float64
	Heartbeats, LogEvents, ConnsMax    int64
	DBStart, DBEnd, RSSMax, MemTotal   int64
}

func summarize(rows []MetricRow) map[string]any {
	var s reportSummary
	var cpuW, cpuN, memW, memN float64
	for _, r := range rows {
		s.Minutes += r.Minutes
		if r.CPUAvg != nil {
			cpuW += *r.CPUAvg * float64(r.Minutes)
			cpuN += float64(r.Minutes)
			if s.CPUMax == nil || *r.CPUMax > *s.CPUMax {
				v := *r.CPUMax
				s.CPUMax = &v
			}
		}
		if r.MemPctAvg != nil {
			memW += *r.MemPctAvg * float64(r.Minutes)
			memN += float64(r.Minutes)
			if s.MemMax == nil || *r.MemPctMax > *s.MemMax {
				v := *r.MemPctMax
				s.MemMax = &v
			}
		}
		// Đầu / cuối kỳ: lấy khoảng đầu tiên / cuối cùng có số liệu (bỏ khoảng trống).
		if r.DiskUsed > 0 {
			if s.DiskStart == 0 {
				s.DiskStart = r.DiskUsed
			}
			s.DiskEnd, s.DiskTotal = r.DiskUsed, r.DiskTotal
		}
		if r.DBSize > 0 {
			if s.DBStart == 0 {
				s.DBStart = r.DBSize
			}
			s.DBEnd = r.DBSize
		}
		if r.MemTotal > 0 {
			s.MemTotal = r.MemTotal
		}
		s.RxBytes += r.RxBytes
		s.TxBytes += r.TxBytes
		s.RxPeak, s.TxPeak = math.Max(s.RxPeak, r.RxPeak), math.Max(s.TxPeak, r.TxPeak)
		s.Heartbeats += r.Heartbeat
		s.LogEvents += r.LogEvents
		s.ConnsMax = max(s.ConnsMax, r.ConnsMax)
		s.RSSMax = max(s.RSSMax, r.RSSMax)
	}
	if cpuN > 0 {
		v := cpuW / cpuN
		s.CPUAvg = &v
	}
	if memN > 0 {
		v := memW / memN
		s.MemAvg = &v
	}
	if s.Minutes > 0 {
		s.RxAvgBps = float64(s.RxBytes) / float64(s.Minutes*60)
		s.TxAvgBps = float64(s.TxBytes) / float64(s.Minutes*60)
	}
	return map[string]any{
		"minutes": s.Minutes, "cpu_avg": s.CPUAvg, "cpu_max": s.CPUMax, "mem_pct_avg": s.MemAvg, "mem_pct_max": s.MemMax,
		"mem_total": s.MemTotal, "disk_start": s.DiskStart, "disk_end": s.DiskEnd, "disk_total": s.DiskTotal,
		"rx_bytes": s.RxBytes, "tx_bytes": s.TxBytes, "rx_avg_bps": s.RxAvgBps, "tx_avg_bps": s.TxAvgBps,
		"rx_peak_bps": s.RxPeak, "tx_peak_bps": s.TxPeak, "heartbeats": s.Heartbeats, "log_events": s.LogEvents,
		"conns_max": s.ConnsMax, "rss_max": s.RSSMax, "db_start": s.DBStart, "db_end": s.DBEnd,
	}
}

var reportSteps = map[string]int{"minute": 60, "10min": 600, "hour": 3600, "day": 86400}

// maxBuckets: số dòng tối đa một báo cáo (chọn bước lớn hơn nếu khoảng dài).
const maxBuckets = 3000

// reportParams đọc from / to (RFC 3339 hoặc YYYY-MM-DD theo giờ server) và step.
func reportParams(r *http.Request) (from, to time.Time, step int, err error) {
	q := r.URL.Query()
	parse := func(s string, def time.Time, endOfDay bool) (time.Time, error) {
		if s == "" {
			return def, nil
		}
		if t, e := time.Parse(time.RFC3339, s); e == nil {
			return t, nil
		}
		t, e := time.ParseInLocation("2006-01-02", s, time.Local)
		if e != nil {
			return t, fmt.Errorf("thời gian không hợp lệ: %q", s)
		}
		if endOfDay {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	now := time.Now()
	if to, err = parse(q.Get("to"), now, true); err != nil {
		return
	}
	if from, err = parse(q.Get("from"), to.Add(-24*time.Hour), false); err != nil {
		return
	}
	if !from.Before(to) || to.Sub(from) > 400*24*time.Hour {
		err = fmt.Errorf("khoảng thời gian không hợp lệ (tối đa 400 ngày)")
		return
	}
	step = reportSteps[q.Get("step")]
	if step == 0 {
		step = 3600
	}
	for int(to.Sub(from).Seconds())/step > maxBuckets { // khoảng dài mà bước nhỏ: tự tăng bước
		switch step {
		case 60:
			step = 600
		case 600:
			step = 3600
		default:
			step = 86400
		}
	}
	return
}

type reportAPI struct{ d Deps }

func (a reportAPI) load(w http.ResponseWriter, r *http.Request) (from, to time.Time, step int, rows []MetricRow, ok bool) {
	ms, has := a.d.Registry.(MetricsStore)
	if !has {
		httpx.WriteError(w, http.StatusServiceUnavailable, "chưa có lịch sử thông số")
		return
	}
	from, to, step, err := reportParams(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	_, off := time.Now().Zone()
	rows, err = ms.ServerReport(r.Context(), from, to, step, off)
	if err != nil {
		a.d.Log.Error("đọc lịch sử thông số thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	if rows == nil {
		rows = []MetricRow{}
	}
	return from, to, step, rows, true
}

// report: GET /admin/v1/server/report?from=&to=&step=minute|10min|hour|day (mức Chỉ xem).
func (a reportAPI) report(w http.ResponseWriter, r *http.Request) {
	from, to, step, rows, ok := a.load(w, r)
	if !ok {
		return
	}
	sum := summarize(rows)
	sum["expected_minutes"] = int(to.Sub(from).Minutes())
	host := ""
	if a.d.Stats != nil {
		host = a.d.Stats.Snapshot().Host.Name
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "from": from, "to": to, "step_s": step,
		"host": host, "rows": rows, "summary": sum})
}

// reportTable: bảng số liệu dùng chung cho CSV và Excel (số để dạng số; MB, Mbit/s, %).
func reportTable(rows []MetricRow, step int) ([]string, [][]any) {
	head := []string{"Thời điểm", "Số phút có số liệu", "CPU TB (%)", "CPU đỉnh (%)", "Tải 1 phút", "RAM TB (%)",
		"RAM đỉnh (%)", "RAM tổng (MB)", "Ổ đĩa đã dùng (MB)", "Ổ đĩa tổng (MB)", "Nhận (MB)", "Gửi (MB)",
		"Nhận TB (Mbit/s)", "Gửi TB (Mbit/s)", "Nhận đỉnh (Mbit/s)", "Gửi đỉnh (Mbit/s)", "Heartbeat", "Sự kiện log",
		"Kết nối agent tối đa", "RAM scs-server tối đa (MB)", "Dung lượng DB (MB)"}
	layout := "2006-01-02 15:04"
	if step >= 86400 {
		layout = "2006-01-02"
	}
	round := func(v float64, n int) float64 { p := math.Pow(10, float64(n)); return math.Round(v*p) / p }
	opt := func(p *float64) any {
		if p == nil {
			return nil
		}
		return round(*p, 1)
	}
	mbv := func(n int64) any { return round(float64(n)/(1<<20), 1) }
	mbit := func(bps float64) any { return round(bps*8/1e6, 3) }
	out := make([][]any, 0, len(rows))
	for _, x := range rows {
		secs := float64(max(x.Minutes, 1) * 60)
		out = append(out, []any{x.At.Local().Format(layout), x.Minutes, opt(x.CPUAvg), opt(x.CPUMax), opt(x.Load1),
			opt(x.MemPctAvg), opt(x.MemPctMax), mbv(x.MemTotal), mbv(x.DiskUsed), mbv(x.DiskTotal), mbv(x.RxBytes),
			mbv(x.TxBytes), mbit(float64(x.RxBytes) / secs), mbit(float64(x.TxBytes) / secs), mbit(x.RxPeak), mbit(x.TxPeak),
			x.Heartbeat, x.LogEvents, x.ConnsMax, mbv(x.RSSMax), mbv(x.DBSize)})
	}
	return head, out
}

func reportName(from, to time.Time, ext string) string {
	return fmt.Sprintf("bao-cao-server_%s_%s.%s", from.Local().Format("20060102-1504"), to.Local().Format("20060102-1504"), ext)
}

// reportCSV: GET /admin/v1/server/report.csv — CSV UTF-8 có BOM, số dùng dấu chấm thập phân.
func (a reportAPI) reportCSV(w http.ResponseWriter, r *http.Request) {
	from, to, step, rows, ok := a.load(w, r)
	if !ok {
		return
	}
	head, data := reportTable(rows, step)
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf") // BOM: Excel mở đúng tiếng Việt
	cw := csv.NewWriter(&buf)
	cw.Write(head)
	for _, rec := range data {
		line := make([]string, len(rec))
		for i, v := range rec {
			switch x := v.(type) {
			case nil:
			case string:
				line[i] = csvSafe(x) // ô chữ: chặn chèn công thức (đồng bộ với báo cáo sự cố); ô số giữ nguyên
			default:
				line[i] = fmt.Sprint(x)
			}
		}
		cw.Write(line)
	}
	cw.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+reportName(from, to, "csv")+`"`)
	w.Write(buf.Bytes())
}

// reportXLSX: GET /admin/v1/server/report.xlsx — file Excel, ô số là số thật (không lệ thuộc dấu thập phân của máy).
func (a reportAPI) reportXLSX(w http.ResponseWriter, r *http.Request) {
	from, to, step, rows, ok := a.load(w, r)
	if !ok {
		return
	}
	head, data := reportTable(rows, step)
	b, err := tabular.WriteXLSX("Bao cao server", head, data)
	if err != nil {
		a.d.Log.Error("tạo file Excel thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+reportName(from, to, "xlsx")+`"`)
	w.Write(b)
}

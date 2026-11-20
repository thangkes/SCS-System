package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/periods"
	"github.com/thangkes/SCS-System/server/internal/tabular"
)

// Báo cáo sự cố: các giai đoạn cảnh báo (sự cố log) và mất liên lạc BẮT ĐẦU trong kỳ — số lượng theo ngày / giờ,
// theo mức, cách đóng, thời gian xử lý, siêu thị / máy / rule gặp nhiều nhất; bảng chi tiết, xuất Excel / CSV.
// Giống Báo cáo server (mức Chỉ xem). Sức khỏe máy không nằm ở đây (chỉ để tham khảo, không phải sự cố).

// IncidentRow: một sự cố trong báo cáo.
type IncidentRow struct {
	ID           int64                       `json:"id"`
	DeviceID     int64                       `json:"device_id"`
	Hostname     string                      `json:"hostname"`
	StoreCode    string                      `json:"store_code,omitempty"`
	StoreName    string                      `json:"store_name,omitempty"`
	Kind         string                      `json:"kind"` // alert | offline
	Severity     string                      `json:"severity,omitempty"`
	StartedAt    time.Time                   `json:"started_at"`
	EndedAt      time.Time                   `json:"ended_at,omitzero"`
	Events       int                         `json:"events"`
	Rules        map[string]*periods.RuleSum `json:"rules,omitempty"`
	ClosedBy     string                      `json:"closed_by,omitempty"`
	Note         string                      `json:"note,omitempty"`
	OfflineSince time.Time                   `json:"offline_since,omitzero"`
	Resolution   string                      `json:"resolution,omitempty"`    // fixed | confirmed (admin đóng)
	Handling     string                      `json:"handling,omitempty"`      // waiting_info | reported
	HandlingNote string                      `json:"handling_note,omitempty"` //
	Titles       map[string]string           `json:"titles,omitempty"`        // tiêu đề rule (kho rule / sức khỏe cũ)
}

// IncidentStore: kho đọc sự cố (PostgreSQL). Trả tối đa limit dòng mới nhất và có bị cắt không.
type IncidentStore interface {
	IncidentReport(ctx context.Context, from, to time.Time, limit int) ([]IncidentRow, bool, error)
}

const incidentLimit = 20000

type incidentAPI struct{ d Deps }

func (a incidentAPI) load(w http.ResponseWriter, r *http.Request) (from, to time.Time, step int, rows []IncidentRow, cut, ok bool) {
	st, has := a.d.Registry.(IncidentStore)
	if !has {
		httpx.WriteError(w, http.StatusServiceUnavailable, "chưa có kho sự cố")
		return
	}
	from, to, _, err := reportParams(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	step = 86400
	if to.Sub(from) <= 48*time.Hour {
		step = 3600
	}
	rows, cut, err = st.IncidentReport(r.Context(), from, to, incidentLimit)
	if err != nil {
		a.d.Log.Error("đọc sự cố thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	titles := map[string]string{}
	if a.d.Rules != nil {
		titles = a.d.Rules.Titles()
	}
	for i := range rows {
		for id := range rows[i].Rules {
			t := titles[id]
			if t == "" {
				t = HealthTitle(id)
			}
			if t != "" {
				if rows[i].Titles == nil {
					rows[i].Titles = map[string]string{}
				}
				rows[i].Titles[id] = t
			}
		}
	}
	if rows == nil {
		rows = []IncidentRow{}
	}
	return from, to, step, rows, cut, true
}

type countItem struct {
	Key    string `json:"key"`
	Label  string `json:"label,omitempty"`
	Count  int    `json:"count"`
	Events int    `json:"events,omitempty"`
}

type incBucket struct {
	At      time.Time `json:"at"`
	Alerts  int       `json:"alerts"`
	Offline int       `json:"offline"`
}

// summarizeIncidents tính tổng hợp cho trang báo cáo.
func summarizeIncidents(rows []IncidentRow, from, to time.Time, step int, now time.Time) map[string]any {
	sev := map[string]int{}
	closed, handling := map[string]int{}, map[string]int{}
	var alerts, offline, open int
	var durs []float64
	stores, devices, rules := map[string]*countItem{}, map[string]*countItem{}, map[string]*countItem{}
	buckets := map[int64]*incBucket{}
	_, off := now.Zone()
	bucketOf := func(t time.Time) int64 {
		s := int64(step)
		return (t.Unix()+int64(off))/s*s - int64(off)
	}
	for b := bucketOf(from); b < to.Unix(); b += int64(step) {
		buckets[b] = &incBucket{At: time.Unix(b, 0)}
	}
	for _, r := range rows {
		if r.Kind == periods.KindOffline {
			offline++
		} else {
			alerts++
			if r.Severity != "" {
				sev[r.Severity]++
			}
		}
		b := buckets[bucketOf(r.StartedAt)]
		if b == nil {
			b = &incBucket{At: time.Unix(bucketOf(r.StartedAt), 0)}
			buckets[bucketOf(r.StartedAt)] = b
		}
		if r.Kind == periods.KindOffline {
			b.Offline++
		} else {
			b.Alerts++
		}
		if r.EndedAt.IsZero() {
			open++
			if r.Handling != "" {
				handling[r.Handling]++
			}
		} else {
			durs = append(durs, r.EndedAt.Sub(r.StartedAt).Minutes())
			switch {
			case r.ClosedBy == periods.ClosedAuto:
				closed["auto"]++
			case strings.HasPrefix(r.ClosedBy, "admin:"):
				closed["admin"]++
				if r.Resolution == periods.ResolutionConfirmed {
					closed["confirmed"]++
				} else {
					closed["fixed"]++
				}
			default:
				closed["system"]++
			}
		}
		if r.StoreCode != "" {
			c := stores[r.StoreCode]
			if c == nil {
				c = &countItem{Key: r.StoreCode, Label: r.StoreName}
				stores[r.StoreCode] = c
			}
			c.Count++
			c.Events += r.Events
		}
		d := devices[r.Hostname]
		if d == nil {
			d = &countItem{Key: r.Hostname}
			devices[r.Hostname] = d
		}
		d.Count++
		d.Events += r.Events
		for id, rs := range r.Rules {
			c := rules[id]
			if c == nil {
				c = &countItem{Key: id, Label: r.Titles[id]}
				rules[id] = c
			}
			c.Count++
			c.Events += rs.Count
		}
	}
	top := func(m map[string]*countItem, n int) []countItem {
		out := make([]countItem, 0, len(m))
		for _, c := range m {
			out = append(out, *c)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Key < out[j].Key
		})
		if len(out) > n {
			out = out[:n]
		}
		return out
	}
	series := make([]incBucket, 0, len(buckets))
	for _, b := range buckets {
		series = append(series, *b)
	}
	sort.Slice(series, func(i, j int) bool { return series[i].At.Before(series[j].At) })
	sum := map[string]any{"total": len(rows), "alerts": alerts, "offline": offline, "open": open, "severity": sev,
		"closed": closed, "handling": handling, "stores": len(stores), "devices": len(devices)}
	if len(durs) > 0 {
		sort.Float64s(durs)
		var t float64
		for _, d := range durs {
			t += d
		}
		sum["avg_minutes"] = math.Round(t / float64(len(durs)))
		sum["median_minutes"] = math.Round(durs[len(durs)/2])
		sum["max_minutes"] = math.Round(durs[len(durs)-1])
	}
	return map[string]any{"summary": sum, "series": series, "top_stores": top(stores, 10), "top_devices": top(devices, 10),
		"top_rules": top(rules, 10)}
}

// report: GET /admin/v1/incidents/report?from=&to= (mức Chỉ xem). Bảng chi tiết: 20 sự cố mới nhất (đủ trong file xuất).
func (a incidentAPI) report(w http.ResponseWriter, r *http.Request) {
	from, to, step, rows, cut, ok := a.load(w, r)
	if !ok {
		return
	}
	out := summarizeIncidents(rows, from, to, step, time.Now())
	latest := rows
	if len(latest) > 20 {
		latest = latest[:20]
	}
	out["ok"], out["from"], out["to"], out["step_s"], out["rows"], out["truncated"] = true, from, to, step, latest, cut
	httpx.WriteJSON(w, http.StatusOK, out)
}

var incKind = map[string]string{periods.KindAlert: "Sự cố (log)", periods.KindOffline: "Mất liên lạc"}
var incSev = map[string]string{"critical": "Nghiêm trọng", "high": "Cao", "medium": "Trung bình", "low": "Thấp", "info": "Thông tin"}

var incHandling = map[string]string{periods.HandlingWaitingInfo: "Chờ thông tin", periods.HandlingReported: "Đã gửi report"}

func closedText(c, resolution, handling string) string {
	switch {
	case c == "":
		if h := incHandling[handling]; h != "" {
			return "Đang mở — " + h
		}
		return "Đang mở"
	case c == periods.ClosedAuto:
		return "Tự đóng (yên đủ lâu)"
	case strings.HasPrefix(c, "admin:"):
		if resolution == periods.ResolutionConfirmed {
			return "Xác nhận sự cố: " + strings.TrimPrefix(c, "admin:")
		}
		return "Đã xử lý: " + strings.TrimPrefix(c, "admin:")
	}
	return "Hệ thống đóng"
}

// incidentTable: bảng dùng chung cho CSV và Excel.
func incidentTable(rows []IncidentRow) ([]string, [][]any) {
	head := []string{"Mã sự cố", "Loại", "Mức", "Bắt đầu", "Kết thúc", "Thời lượng (phút)", "Máy", "Mã siêu thị", "Tên siêu thị",
		"Số sự kiện", "Rule chính", "Tiêu đề rule chính", "Các rule (số lần)", "Mất liên lạc từ", "Đóng bởi", "Ghi chú"}
	const layout = "2006-01-02 15:04"
	out := make([][]any, 0, len(rows))
	for _, r := range rows {
		ended, dur := any(nil), any(nil)
		if !r.EndedAt.IsZero() {
			ended = r.EndedAt.Local().Format(layout)
			dur = math.Round(r.EndedAt.Sub(r.StartedAt).Minutes())
		}
		type kv struct {
			id string
			n  int
		}
		var list []kv
		for id, rs := range r.Rules {
			list = append(list, kv{id, rs.Count})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n || list[i].n == list[j].n && list[i].id < list[j].id })
		main, mainTitle := "", ""
		parts := make([]string, 0, len(list))
		for i, x := range list {
			if i == 0 {
				main, mainTitle = x.id, r.Titles[x.id]
			}
			parts = append(parts, fmt.Sprintf("%s ×%d", x.id, x.n))
		}
		offSince := any(nil)
		if !r.OfflineSince.IsZero() {
			offSince = r.OfflineSince.Local().Format(layout)
		}
		out = append(out, []any{r.ID, incKind[r.Kind], incSev[r.Severity], r.StartedAt.Local().Format(layout), ended, dur, r.Hostname,
			r.StoreCode, r.StoreName, r.Events, main, mainTitle, strings.Join(parts, "; "), offSince, closedText(r.ClosedBy, r.Resolution, r.Handling), noteText(r)})
	}
	return head, out
}

// noteText: ghi chú khi đóng; sự cố đang mở có trạng thái xử lý thì lấy ghi chú của trạng thái đó.
func noteText(r IncidentRow) string {
	if r.EndedAt.IsZero() && r.HandlingNote != "" {
		return r.HandlingNote
	}
	return r.Note
}

func incidentName(from, to time.Time, ext string) string {
	return fmt.Sprintf("bao-cao-su-co_%s_%s.%s", from.Local().Format("20060102-1504"), to.Local().Format("20060102-1504"), ext)
}

// reportCSV: GET /admin/v1/incidents/report.csv — CSV UTF-8 có BOM.
func (a incidentAPI) reportCSV(w http.ResponseWriter, r *http.Request) {
	from, to, _, rows, _, ok := a.load(w, r)
	if !ok {
		return
	}
	head, data := incidentTable(rows)
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf")
	cw := csv.NewWriter(&buf)
	cw.Write(head)
	for _, rec := range data {
		line := make([]string, len(rec))
		for i, v := range rec {
			if v != nil {
				line[i] = csvSafe(fmt.Sprint(v))
			}
		}
		cw.Write(line)
	}
	cw.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+incidentName(from, to, "csv")+`"`)
	w.Write(buf.Bytes())
}

// csvSafe: chặn chèn công thức khi mở CSV bằng Excel (ghi chú / dòng log do người hoặc máy nhập).
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// reportXLSX: GET /admin/v1/incidents/report.xlsx — Excel, ô số là số thật.
func (a incidentAPI) reportXLSX(w http.ResponseWriter, r *http.Request) {
	from, to, _, rows, _, ok := a.load(w, r)
	if !ok {
		return
	}
	head, data := incidentTable(rows)
	b, err := tabular.WriteXLSX("Bao cao su co", head, data)
	if err != nil {
		a.d.Log.Error("tạo file Excel thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+incidentName(from, to, "xlsx")+`"`)
	w.Write(b)
}

package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/server/internal/periods"
)

// incReg: kho giả có thêm IncidentReport.
type incReg struct {
	*fakeRegistry
	rows []IncidentRow
}

func (r incReg) IncidentReport(_ context.Context, from, to time.Time, limit int) ([]IncidentRow, bool, error) {
	return r.rows, false, nil
}

func sampleIncidents(now time.Time) []IncidentRow {
	return []IncidentRow{
		{ID: 3, Hostname: "12345-TN1-PRI", StoreCode: "12345", StoreName: "Siêu thị A", Kind: periods.KindAlert, Severity: "critical",
			StartedAt: now.Add(-2 * time.Hour), Events: 5, Rules: map[string]*periods.RuleSum{"DISK-IO-ERR-01": {Count: 4}, "SVC-01": {Count: 1}},
			Note: "=CMD()"},
		{ID: 2, Hostname: "12345-TN2-SEC", StoreCode: "12345", Kind: periods.KindOffline, StartedAt: now.Add(-5 * time.Hour),
			EndedAt: now.Add(-4 * time.Hour), ClosedBy: "system"},
		{ID: 1, Hostname: "67890-TN1-PRI", StoreCode: "67890", Kind: periods.KindAlert, Severity: "high", StartedAt: now.Add(-30 * time.Hour),
			EndedAt: now.Add(-6 * time.Hour), ClosedBy: "admin:An", Events: 2, Rules: map[string]*periods.RuleSum{"DISK-IO-ERR-01": {Count: 2}}},
	}
}

// Tổng hợp: đếm theo loại / mức / cách đóng, thời gian xử lý, top siêu thị / rule, chuỗi theo giờ đủ mọi mốc.
func TestSummarizeIncidents(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	rows := sampleIncidents(now)
	out := summarizeIncidents(rows, now.Add(-48*time.Hour), now, 3600, now)
	sum := out["summary"].(map[string]any)
	if sum["total"] != 3 || sum["alerts"] != 2 || sum["offline"] != 1 || sum["open"] != 1 {
		t.Fatalf("đếm: %+v", sum)
	}
	if sum["severity"].(map[string]int)["critical"] != 1 || sum["closed"].(map[string]int)["admin"] != 1 || sum["closed"].(map[string]int)["system"] != 1 {
		t.Fatalf("mức / cách đóng: %+v", sum)
	}
	if sum["max_minutes"].(float64) != 1440 || sum["avg_minutes"].(float64) != 750 {
		t.Fatalf("thời gian xử lý: %+v", sum)
	}
	if ts := out["top_stores"].([]countItem); ts[0].Key != "12345" || ts[0].Count != 2 {
		t.Fatalf("top siêu thị: %+v", ts)
	}
	if tr := out["top_rules"].([]countItem); tr[0].Key != "DISK-IO-ERR-01" || tr[0].Count != 2 || tr[0].Events != 6 {
		t.Fatalf("top rule: %+v", tr)
	}
	series := out["series"].([]incBucket)
	total := 0
	for _, b := range series {
		total += b.Alerts + b.Offline
	}
	if len(series) < 48 || total != 3 {
		t.Fatalf("chuỗi theo giờ: %d mốc, %d sự cố", len(series), total)
	}
}

// API: JSON tổng hợp + 20 dòng; CSV có BOM, chặn chèn công thức; Excel tải được; mức Chỉ xem là đủ.
func TestIncidentReportAPI(t *testing.T) {
	d, _ := deps(t, nil)
	now := time.Now()
	d.Registry = incReg{newFakeRegistry(), sampleIncidents(now)}
	h := AdminHandler(d)
	get := func(path string) *httptest.ResponseRecorder {
		return do(t, h, "GET", path, map[string]string{"Authorization": "Bearer " + testToken})
	}
	q := "?from=" + now.Add(-7*24*time.Hour).UTC().Format(time.RFC3339) + "&to=" + now.UTC().Format(time.RFC3339)
	w := get("/admin/v1/incidents/report" + q)
	var j struct {
		Summary map[string]any `json:"summary"`
		Rows    []IncidentRow  `json:"rows"`
		Step    int            `json:"step_s"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &j) != nil || len(j.Rows) != 3 || j.Step != 86400 || j.Summary["total"].(float64) != 3 {
		t.Fatalf("JSON: %d %s", w.Code, w.Body)
	}
	w = get("/admin/v1/incidents/report.csv" + q)
	body := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(body, "\xef\xbb\xbf") || !strings.Contains(body, "'=CMD()") || !strings.Contains(body, "Admin xử lý: An") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "bao-cao-su-co_") {
		t.Fatalf("CSV: %d %s", w.Code, body)
	}
	if w = get("/admin/v1/incidents/report.xlsx" + q); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "PK") {
		t.Fatalf("Excel: %d", w.Code)
	}
	if w = get("/admin/v1/incidents/report?from=x"); w.Code != 400 {
		t.Fatalf("tham số sai: %d", w.Code)
	}
}

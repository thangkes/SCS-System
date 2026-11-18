package app

import (
	"net/http/httptest"
	"testing"
	"time"
)

func fp(v float64) *float64 { return &v }

func TestReportParams(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?from=2026-11-01&to=2026-11-07&step=hour", nil)
	from, to, step, err := reportParams(r)
	if err != nil {
		t.Fatal(err)
	}
	if to.Sub(from) != 7*24*time.Hour || step != 3600 || from.Hour() != 0 {
		t.Fatalf("ngày theo giờ server, 'to' tính hết ngày: %v %v %d", from, to, step)
	}
	// 30 ngày mà xin từng phút: tự tăng bước để không quá maxBuckets dòng.
	r = httptest.NewRequest("GET", "/x?from=2026-10-01&to=2026-10-30&step=minute", nil)
	if _, _, step, _ = reportParams(r); step != 3600 {
		t.Fatalf("bước tự tăng: %d", step)
	}
	for _, q := range []string{"from=abc", "from=2026-11-07&to=2026-11-01", "from=2020-01-01&to=2026-01-01"} {
		if _, _, _, err := reportParams(httptest.NewRequest("GET", "/x?"+q, nil)); err == nil {
			t.Errorf("%s phải lỗi", q)
		}
	}
	if _, _, step, err := reportParams(httptest.NewRequest("GET", "/x", nil)); err != nil || step != 3600 {
		t.Fatalf("mặc định 24 giờ, theo giờ: %d %v", step, err)
	}
}

func TestSummarize(t *testing.T) {
	rows := []MetricRow{
		{Minutes: 60, CPUAvg: fp(10), CPUMax: fp(40), MemPctAvg: fp(20), MemPctMax: fp(25), DiskUsed: 100, DBSize: 5,
			RxBytes: 600, TxBytes: 60, RxPeak: 50, Heartbeat: 10},
		{Minutes: 30, CPUAvg: fp(40), CPUMax: fp(90), MemPctAvg: fp(50), MemPctMax: fp(60), DiskUsed: 130, DBSize: 7,
			RxBytes: 300, TxBytes: 30, RxPeak: 80, Heartbeat: 5},
		{Minutes: 0}, // khoảng không có số liệu CPU / RAM
	}
	s := summarize(rows)
	if s["minutes"] != 90 || *(s["cpu_avg"].(*float64)) != 20 || *(s["cpu_max"].(*float64)) != 90 {
		t.Fatalf("CPU (trung bình theo số phút): %v", s)
	}
	if *(s["mem_pct_avg"].(*float64)) != 30 || s["disk_start"] != int64(100) || s["disk_end"] != int64(130) || s["db_end"] != int64(7) {
		t.Fatalf("RAM / ổ đĩa: %v", s)
	}
	if s["rx_bytes"] != int64(900) || s["rx_avg_bps"] != float64(900)/(90*60) || s["rx_peak_bps"] != float64(80) || s["heartbeats"] != int64(15) {
		t.Fatalf("mạng / heartbeat: %v", s)
	}
}

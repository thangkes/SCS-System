package heartbeat

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Máy vừa khởi động: heartbeat đầu dời ngẫu nhiên 5–30 phút, theo giây, phân bố đều; khởi động lại giữa
// chừng (uptime lớn) thì gọi ngay; heartbeat gần nhất đã lâu thì cắt để không quá MaxGap (không dưới 5 phút).
func TestBootDelay(t *testing.T) {
	lo, hi := time.Duration(1<<62), time.Duration(0)
	buckets := map[int]int{}
	for i := 0; i < 20000; i++ {
		d := BootDelay(2*time.Minute, 0, false)
		if d < BootDelayMin || d > BootDelayMax || d%time.Second != 0 {
			t.Fatalf("ngoài 5–30 phút hoặc không tròn giây: %s", d)
		}
		lo, hi = min(lo, d), max(hi, d)
		buckets[int((d-BootDelayMin)/(5*time.Minute))]++
	}
	if lo > BootDelayMin+30*time.Second || hi < BootDelayMax-30*time.Second {
		t.Fatalf("phải trải đủ 5–30 phút: %s – %s", lo, hi)
	}
	for b := 0; b < 5; b++ { // 5 khoảng 5 phút, mỗi khoảng ~20%
		if n := buckets[b]; n < 3000 || n > 5000 {
			t.Fatalf("phân bố không đều: khoảng %d có %d / 20000", b, n)
		}
	}
	// Không phải vừa khởi động: gọi ngay.
	if d := BootDelay(BootWindow, 0, false); d != 0 {
		t.Fatalf("uptime %s: phải gọi ngay, được %s", BootWindow, d)
	}
	if d := BootDelay(3*time.Hour, 0, false); d != 0 {
		t.Fatalf("restart giữa ngày phải gọi ngay: %s", d)
	}
	// Heartbeat gần nhất cách đây 25 phút: chờ tối đa 45 − 25 = 20 phút.
	for i := 0; i < 2000; i++ {
		if d := BootDelay(time.Minute, 25*time.Minute, true); d < BootDelayMin || d > 20*time.Minute {
			t.Fatalf("phải cắt ở 20 phút: %s", d)
		}
	}
	// Đã quá 45 phút (máy tắt lâu): vẫn chờ đúng 5 phút tối thiểu.
	if d := BootDelay(time.Minute, 3*time.Hour, true); d != BootDelayMin {
		t.Fatalf("máy tắt lâu: phải chờ đúng 5 phút, được %s", d)
	}
}

func TestLastHeartbeatFile(t *testing.T) {
	dir := t.TempDir()
	if _, ok := LastHeartbeat(dir); ok {
		t.Fatal("chưa có file")
	}
	now := time.Unix(1795000000, 0)
	saveLast(dir, now)
	if got, ok := LastHeartbeat(dir); !ok || !got.Equal(now) {
		t.Fatalf("đọc lại: %v %v", got, ok)
	}
}

// Run chờ FirstDelay trước khi gửi gì; hủy trong lúc chờ thì thoát ngay, không gọi server.
func TestRunWaitsFirstDelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	// Client nil: nếu Run không chờ mà gọi server thì panic.
	err := Run(ctx, Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), FirstDelay: time.Hour})
	if err != context.DeadlineExceeded || time.Since(start) > time.Second {
		t.Fatalf("phải chờ rồi thoát khi hủy: %v sau %s", err, time.Since(start))
	}
}

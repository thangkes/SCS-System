package heartbeat

import (
	"testing"
	"time"
)

// Heartbeat thành công: chờ ngẫu nhiên 5–10 phút (nhịp server 5 phút); lỗi: ±20%.
func TestNextWait(t *testing.T) {
	base := 5 * time.Minute
	lo, hi := time.Duration(1<<62), time.Duration(0)
	for i := 0; i < 2000; i++ {
		w := nextWait(base, true)
		if w < base || w > 2*base {
			t.Fatalf("ngoài khoảng 5–10 phút: %s", w)
		}
		lo, hi = min(lo, w), max(hi, w)
	}
	if lo > 6*time.Minute || hi < 9*time.Minute {
		t.Fatalf("không trải đều khoảng 5–10 phút: %s – %s", lo, hi)
	}
	for i := 0; i < 500; i++ {
		if w := nextWait(time.Minute, false); w < 48*time.Second || w > 72*time.Second {
			t.Fatalf("thử lại sau lỗi phải ±20%%: %s", w)
		}
	}
	if nextWait(0, true) != 0 {
		t.Fatal("0")
	}
}

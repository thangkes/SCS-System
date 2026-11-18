package heartbeat

import (
	"testing"
	"time"
)

// Heartbeat thành công: chờ ngẫu nhiên 5–10 phút (nhịp server 5 phút); lỗi: ±20%.
func TestNextWait(t *testing.T) {
	base, hi := 5*time.Minute, 30*time.Minute
	lo, top := time.Duration(1<<62), time.Duration(0)
	for i := 0; i < 5000; i++ {
		w := nextWait(base, hi, true)
		if w < base || w > hi || w%time.Second != 0 {
			t.Fatalf("ngoài khoảng 5–30 phút hoặc không tròn giây: %s", w)
		}
		lo, top = min(lo, w), max(top, w)
	}
	if lo > 6*time.Minute || top < 29*time.Minute {
		t.Fatalf("không trải đều khoảng 5–30 phút: %s – %s", lo, top)
	}
	if w := nextWait(base, base, true); w != base {
		t.Fatalf("hi = d thì chờ đúng d: %s", w)
	}
	for i := 0; i < 500; i++ {
		if w := nextWait(time.Minute, 0, false); w < 48*time.Second || w > 72*time.Second {
			t.Fatalf("thử lại sau lỗi phải ±20%%: %s", w)
		}
	}
	if nextWait(0, 0, true) != 0 {
		t.Fatal("0")
	}
}

func TestNeedRenew(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	cases := []struct {
		left time.Duration
		last time.Time
		want bool
	}{
		{60 * day, time.Time{}, false},                // còn lâu
		{29 * day, time.Time{}, true},                 // vào khoảng gia hạn
		{29 * day, now.Add(-10 * time.Minute), false}, // vừa thử: chờ RenewRetry
		{29 * day, now.Add(-2 * time.Hour), true},
		{-time.Hour, now.Add(-time.Minute), true}, // đã hết hạn: thử mỗi lần
	}
	for i, c := range cases {
		if got := needRenew(c.left, c.last, now); got != c.want {
			t.Errorf("ca %d: %v, muốn %v", i, got, c.want)
		}
	}
}

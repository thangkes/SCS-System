package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Giới hạn dòng log: tối đa logBurst dòng dồn dập, sau đó 1 dòng mỗi logRefill; dòng bị bỏ được đếm.
func TestLogLimit(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(1_000_000, 0)
	h := newLimitHandler(slog.NewJSONHandler(&buf, nil), func() time.Time { return now })
	log := slog.New(h).With("version", "x")
	for i := 0; i < 1000; i++ {
		log.Warn("lỗi lặp")
	}
	if n := strings.Count(buf.String(), "\n"); n != logBurst {
		t.Fatalf("ghi %d dòng, muốn %d", n, logBurst)
	}
	buf.Reset()
	now = now.Add(logRefill)
	log.Warn("sau 10 giây")
	if !strings.Contains(buf.String(), `"log_bo_qua":900`) {
		t.Fatalf("dòng kế tiếp phải báo đã bỏ 900 dòng: %s", buf.String())
	}
	// Một ngày dồn dập: không quá logBurst + 86400/10 dòng.
	buf.Reset()
	for i := 0; i < 86400; i++ {
		now = now.Add(time.Second)
		log.Error("lỗi mỗi giây")
	}
	if n := strings.Count(buf.String(), "\n"); n > logBurst+8640 {
		t.Fatalf("một ngày ghi %d dòng, quá giới hạn", n)
	}
}

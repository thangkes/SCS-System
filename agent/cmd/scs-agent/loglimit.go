package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Giới hạn số dòng log của agent: lớp bảo vệ cuối để agent không làm phình journal của máy, kể cả khi một chỗ nào đó
// báo lỗi dồn dập (ổ hỏng, mất mạng kéo dài…). Thùng token: đầy logBurst dòng, hồi 1 dòng mỗi logRefill — tối đa
// khoảng 100 + 8.640 dòng / ngày (vài MB) dù có chuyện gì. Dòng bị bỏ được đếm và báo ở dòng log kế tiếp được ghi.
const (
	logBurst  = 100
	logRefill = 10 * time.Second
)

type limitHandler struct {
	next slog.Handler
	st   *limitState
}

type limitState struct {
	mu      sync.Mutex
	tokens  float64
	last    time.Time
	dropped int
	now     func() time.Time
}

func newLimitHandler(next slog.Handler, now func() time.Time) *limitHandler {
	if now == nil {
		now = time.Now
	}
	return &limitHandler{next: next, st: &limitState{tokens: logBurst, last: now(), now: now}}
}

func (h *limitHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *limitHandler) Handle(ctx context.Context, r slog.Record) error {
	s := h.st
	s.mu.Lock()
	now := s.now()
	s.tokens = min(logBurst, s.tokens+now.Sub(s.last).Seconds()/logRefill.Seconds())
	s.last = now
	if s.tokens < 1 {
		s.dropped++
		s.mu.Unlock()
		return nil
	}
	s.tokens--
	dropped := s.dropped
	s.dropped = 0
	s.mu.Unlock()
	if dropped > 0 {
		r = r.Clone()
		r.AddAttrs(slog.Int("log_bo_qua", dropped)) // số dòng log đã bỏ vì vượt giới hạn
	}
	return h.next.Handle(ctx, r)
}

func (h *limitHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &limitHandler{next: h.next.WithAttrs(a), st: h.st}
}

func (h *limitHandler) WithGroup(name string) slog.Handler {
	return &limitHandler{next: h.next.WithGroup(name), st: h.st}
}

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/thangkes/SCS-System/server/internal/auth"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
)

// Thông báo tức thời trên web quản trị (không dùng dịch vụ ngoài): trình duyệt mở một luồng
// Server-Sent Events (GET /admin/v1/stream, cùng cổng 7222, cùng phiên đăng nhập). Một tiến trình
// chung hỏi DB vài giây một lần — chỉ khi có trình duyệt đang mở — và đẩy "pulse" xuống mọi trình
// duyệt khi số liệu đổi: số cảnh báo, máy chờ duyệt, mất liên lạc, bộ rule cần ký, các cảnh báo vừa
// mở / vừa cập nhật. Hỏi DB thay vì móc vào từng chỗ ghi nên đúng cả khi cổng agent và web chạy hai
// tiến trình.

const (
	liveEvery     = 3 * time.Second  // nhịp hỏi DB khi có người xem
	liveRulesEach = 5                // đếm bộ rule cần ký mỗi 5 nhịp (đọc thư mục)
	livePing      = 25 * time.Second // dòng giữ kết nối (proxy, NAT không cắt)
	liveRecheck   = 30 * time.Second // kiểm lại phiên đăng nhập (đăng xuất / thu hồi thì ngắt)
	liveMaxSubs   = 64               // số trình duyệt mở luồng cùng lúc tối đa
	liveWrite     = 15 * time.Second // hạn ghi mỗi lần đẩy (máy xem quá chậm thì ngắt)
)

// LivePulse: gói đẩy xuống trình duyệt.
type LivePulse struct {
	Pulse
	RulesPending int       `json:"rules_pending"`
	At           time.Time `json:"at"`
}

// Live: nơi phát thông báo tức thời. Tạo bằng NewLive, chạy Run trong suốt đời tiến trình.
type Live struct {
	d      Deps
	mu     sync.Mutex
	subs   map[chan []byte]struct{}
	last   []byte // gói gần nhất (gửi ngay cho trình duyệt mới mở)
	since  time.Time
	sig    string
	rules  int
	tick   int
	wake   chan struct{}
	done   chan struct{}
	closed bool
}

// NewLive tạo nơi phát thông báo. d.Registry nil thì luồng chỉ gửi dòng giữ kết nối.
func NewLive(d Deps) *Live {
	return &Live{d: d, subs: map[chan []byte]struct{}{}, since: time.Now(), wake: make(chan struct{}, 1),
		done: make(chan struct{})}
}

// Run hỏi số liệu định kỳ tới khi ctx kết thúc; lúc đó đóng mọi luồng đang mở (tắt server không phải
// chờ trình duyệt).
func (l *Live) Run(ctx context.Context) error {
	t := time.NewTicker(liveEvery)
	defer t.Stop()
	defer func() {
		l.mu.Lock()
		l.closed = true
		close(l.done)
		l.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-l.wake: // trình duyệt đầu tiên vừa mở: hỏi ngay
		}
		l.mu.Lock()
		n := len(l.subs)
		l.mu.Unlock()
		if n == 0 {
			continue
		}
		l.poll(ctx)
	}
}

func (l *Live) poll(ctx context.Context) {
	if l.d.Registry == nil {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := l.d.Registry.Pulse(qctx, l.since)
	if err != nil {
		if ctx.Err() == nil {
			l.d.Log.Warn("thông báo tức thời: không đọc được số liệu", "err", err)
		}
		return
	}
	if l.tick%liveRulesEach == 0 && l.d.Rules != nil {
		if list, err := l.d.Rules.List(); err == nil {
			n := 0
			for _, e := range list {
				if e.Status == rulestore.StatusUnsigned || e.Status == rulestore.StatusStale {
					n++
				}
			}
			l.rules = n
		}
	}
	l.tick++
	sig := fmt.Sprintf("%d/%d/%d/%d/%d/%s", p.OpenAlerts, p.Serious, p.Offline, p.Pending, l.rules,
		p.LastChange.UTC().Format(time.RFC3339Nano))
	if p.LastChange.After(l.since) {
		l.since = p.LastChange
	}
	l.mu.Lock()
	first := l.last == nil
	l.mu.Unlock()
	if sig == l.sig && !first {
		return
	}
	l.sig = sig
	b, _ := json.Marshal(LivePulse{Pulse: p, RulesPending: l.rules, At: time.Now().UTC()})
	l.mu.Lock()
	l.last = b
	for ch := range l.subs {
		select {
		case ch <- b:
		default: // trình duyệt không đọc kịp: bỏ gói này, gói sau mang số liệu mới hơn
		}
	}
	l.mu.Unlock()
}

func (l *Live) subscribe() (chan []byte, []byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || len(l.subs) >= liveMaxSubs {
		return nil, nil, false
	}
	ch := make(chan []byte, 4)
	l.subs[ch] = struct{}{}
	if len(l.subs) == 1 {
		select {
		case l.wake <- struct{}{}:
		default:
		}
	}
	return ch, l.last, true
}

func (l *Live) unsubscribe(ch chan []byte) {
	l.mu.Lock()
	delete(l.subs, ch)
	l.mu.Unlock()
}

// Handler: GET /admin/v1/stream (quyền Chỉ xem trở lên).
func (l *Live) Handler(am *auth.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch, last, ok := l.subscribe()
		if !ok {
			httpx.WriteError(w, http.StatusServiceUnavailable, "quá nhiều trình duyệt đang mở luồng thông báo")
			return
		}
		defer l.unsubscribe(ch)
		rc := http.NewResponseController(w)
		h := w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		send := func(s string) bool {
			_ = rc.SetWriteDeadline(time.Now().Add(liveWrite))
			if _, err := w.Write([]byte(s)); err != nil {
				return false
			}
			return rc.Flush() == nil
		}
		// retry: trình duyệt tự nối lại sau 5 giây khi mất kết nối.
		if !send("retry: 5000\n\n") {
			return
		}
		if last != nil && !send("event: pulse\ndata: "+string(last)+"\n\n") {
			return
		}
		ping := time.NewTicker(livePing)
		defer ping.Stop()
		recheck := time.NewTicker(liveRecheck)
		defer recheck.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-l.done:
				return
			case b := <-ch:
				if !send("event: pulse\ndata: " + string(b) + "\n\n") {
					return
				}
			case <-ping.C:
				if !send(": ping\n\n") {
					return
				}
			case <-recheck.C:
				// Đã đăng xuất / bị thu hồi phiên / hết hạn 24 giờ: ngắt luồng.
				if am != nil {
					if _, ok := am.Identify(r); !ok {
						send("event: logout\ndata: {}\n\n")
						return
					}
				}
			}
		}
	})
}

package app

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Downloads: phân lượt tải bản agent và giới hạn băng thông tải (bản agent + bộ rule) của server.
//
//   - Lượt tải: tối đa Max máy được tải bản agent cùng lúc (SCS_DOWNLOAD_SLOTS, mặc định 4). Heartbeat chỉ
//     báo "có bản mới" cho máy giữ được lượt; máy khác chạy bản cũ bình thường và xin lại ở heartbeat sau
//     (5–30 phút). Lượt được trả khi tải xong, hoặc tự thu lại sau slotTTL nếu máy không tải.
//   - Băng thông: tổng phần thân file tải (bản agent, bộ rule) không quá SCS_DOWNLOAD_MBPS (mặc định
//     20 Mbit/s); heartbeat, log, web không bị giới hạn.
//
// Mọi phương thức an toàn với *Downloads nil (không giới hạn), để test và cấu hình cũ chạy như trước.
type Downloads struct {
	mu      sync.Mutex
	now     func() time.Time
	max     int
	rate    float64 // byte / giây
	tokens  float64
	last    time.Time
	holders map[int64]time.Time // máy → hạn giữ lượt
	// Đo tốc độ thực: byte đã gửi theo từng giây (10 giây gần nhất).
	sec   [10]int64
	secAt [10]int64
	// fails: máy báo tải / kiểm bản agent lỗi (heartbeat), theo bản; mất khi server khởi động lại.
	fails map[int64]UpdateFailure
	// Đếm từ lúc khởi động: lượt tải xong / bị ngắt, lượt bị từ chối vì hết chỗ.
	done, aborted, busy int64
}

// UpdateFailure: máy báo không cài được bản agent (tải hoặc kiểm chữ ký lỗi).
type UpdateFailure struct {
	Version string    `json:"version"`
	Error   string    `json:"error"`
	At      time.Time `json:"at"`
}

const (
	slotTTL       = 15 * time.Minute // máy giữ lượt mà không tải xong trong bấy lâu thì thu lại
	downloadChunk = 16 << 10
	downloadBurst = 256 << 10
)

func NewDownloads(slots int, mbps float64) *Downloads {
	return &Downloads{now: time.Now, max: max(slots, 1), rate: max(mbps, 0.1) * 1e6 / 8,
		tokens: downloadBurst, holders: map[int64]time.Time{}, fails: map[int64]UpdateFailure{}}
}

// Acquire: máy xin lượt tải bản agent. Đang giữ lượt (chưa hết hạn) thì vẫn được, nhưng KHÔNG gia hạn:
// máy heartbeat đều mà không tải (lỗi, bị chặn) không giữ lượt mãi. Còn chỗ thì cấp lượt mới 15 phút.
func (g *Downloads) Acquire(dev int64) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for id, exp := range g.holders {
		if now.After(exp) {
			delete(g.holders, id)
		}
	}
	if _, ok := g.holders[dev]; ok {
		return true
	}
	if len(g.holders) < g.max {
		g.holders[dev] = now.Add(slotTTL)
		return true
	}
	g.busy++
	return false
}

// Release trả lượt (tải xong hoặc bị ngắt).
func (g *Downloads) Release(dev int64, complete bool) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.holders, dev)
	if complete {
		g.done++
	} else {
		g.aborted++
	}
	g.mu.Unlock()
}

// Free trả lượt mà không tính vào số lượt tải (máy mới nhận chứng chỉ lúc hòa mạng).
func (g *Downloads) Free(dev int64) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.holders, dev)
	g.mu.Unlock()
}

// Report: heartbeat báo lỗi cài bản agent (nil = không lỗi, xóa lỗi cũ của máy).
func (g *Downloads) Report(dev int64, f *UpdateFailure) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if f == nil {
		delete(g.fails, dev)
	} else {
		g.fails[dev] = *f
	}
	g.mu.Unlock()
}

// wait chờ đủ "vé" để gửi n byte (giới hạn tổng băng thông tải). Trả lỗi khi request bị hủy.
func (g *Downloads) wait(ctx context.Context, n int) error {
	g.mu.Lock()
	now := g.now()
	g.tokens = min(downloadBurst, g.tokens+now.Sub(g.last).Seconds()*g.rate)
	g.last = now
	g.tokens -= float64(n)
	var d time.Duration
	if g.tokens < 0 {
		d = time.Duration(-g.tokens / g.rate * float64(time.Second))
	}
	s := now.Unix()
	i := s % int64(len(g.sec))
	if g.secAt[i] != s {
		g.secAt[i], g.sec[i] = s, 0
	}
	g.sec[i] += int64(n)
	g.mu.Unlock()
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// DownloadStats: số liệu cho trang Agent.
type DownloadStats struct {
	Slots     int     `json:"slots"`      // số lượt tối đa
	InUse     int     `json:"in_use"`     // lượt đang giữ
	LimitMbps float64 `json:"limit_mbps"` // giới hạn băng thông tải
	NowMbps   float64 `json:"now_mbps"`   // tốc độ tải trung bình 10 giây gần nhất
	Done      int64   `json:"done"`       // lượt tải xong từ lúc server khởi động
	Aborted   int64   `json:"aborted"`    // lượt bị ngắt giữa chừng
	Busy      int64   `json:"busy"`       // lần máy xin lượt mà hết chỗ (máy xin lại ở heartbeat sau)
	// Failures: máy đang báo lỗi cài bản agent, theo phiên bản (từ lúc server khởi động).
	Failures map[string]int `json:"failures"`
}

func (g *Downloads) Stats() *DownloadStats {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	inUse := 0
	for _, exp := range g.holders {
		if !now.After(exp) {
			inUse++
		}
	}
	var sum int64
	cur := now.Unix()
	for i := range g.sec {
		if cur-g.secAt[i] < int64(len(g.sec)) && g.secAt[i] != cur { // bỏ giây đang dở
			sum += g.sec[i]
		}
	}
	fails := map[string]int{}
	for _, f := range g.fails {
		fails[f.Version]++
	}
	return &DownloadStats{Slots: g.max, InUse: inUse, LimitMbps: g.rate * 8 / 1e6,
		NowMbps: float64(sum) * 8 / 1e6 / float64(len(g.sec)-1), Done: g.done, Aborted: g.aborted, Busy: g.busy,
		Failures: fails}
}

// throttle bọc ResponseWriter: phần thân gửi đi đi qua giới hạn băng thông chung.
func (g *Downloads) throttle(w http.ResponseWriter, r *http.Request) http.ResponseWriter {
	if g == nil {
		return w
	}
	return &throttledWriter{ResponseWriter: w, g: g, ctx: r.Context()}
}

type throttledWriter struct {
	http.ResponseWriter
	g       *Downloads
	ctx     context.Context
	written int64
}

func (t *throttledWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		c := min(len(p), downloadChunk)
		if err := t.g.wait(t.ctx, c); err != nil {
			return n, err
		}
		m, err := t.ResponseWriter.Write(p[:c])
		n += m
		t.written += int64(m)
		if err != nil {
			return n, err
		}
		p = p[c:]
	}
	return n, nil
}

package httpx

import (
	"context"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// PublicLogWindow: lỗi cùng loại trong khoảng này chỉ ghi chi tiết lần đầu, còn lại gộp
// thành một dòng thống kê cuối cửa sổ. Test đổi được.
var PublicLogWindow = 30 * time.Second

// Giới hạn số IP / số loại lỗi được đếm riêng trong một cửa sổ (bị dò từ rất nhiều IP
// cũng không phình bộ nhớ).
const (
	publicLogMaxIPs   = 1000
	publicLogMaxKinds = 50
)

// PublicAccessLog là access log cho cổng agent (mở ra Internet): request thành công ở
// mức debug (20.000 máy gọi đều đặn, ghi hết thì log quá lớn), request lỗi được gộp.
func PublicAccessLog(log *slog.Logger) Middleware {
	return AggregatedAccessLog(log, slog.LevelDebug)
}

// AggregatedAccessLog ghi access log mà bị gửi dồn dập request lỗi (404, 401, 429...)
// cũng không làm đầy ổ đĩa hay làm journald vứt log thật:
//   - request thành công: mức okLevel (health check luôn ở mức debug)
//   - request lỗi: mỗi mã trạng thái ghi chi tiết lần đầu trong cửa sổ, các lần sau
//     gộp thành một dòng cuối cửa sổ: số lượng theo mã, IP gửi nhiều nhất.
func AggregatedAccessLog(log *slog.Logger, okLevel slog.Level) Middleware {
	agg := &errAggregator{log: log, msg: "request lỗi (đã gộp, chỉ ghi chi tiết lần đầu mỗi mã)", key: "theo_ma"}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			if sw.status == 0 {
				sw.status = http.StatusOK
			}
			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", truncatePath(r.URL.Path)),
				slog.Int("status", sw.status),
				slog.Int("bytes", sw.bytes),
				slog.Int64("ms", time.Since(start).Milliseconds()),
				slog.String("remote", r.RemoteAddr),
				slog.String("req_id", RequestID(r.Context())),
			}
			if sw.status < 400 {
				level := okLevel
				if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
					level = slog.LevelDebug
				}
				log.LogAttrs(r.Context(), level, "request", attrs...)
				return
			}
			kind := http.StatusText(sw.status) + " (" + itoa(sw.status) + ")"
			if agg.first(kind, sw.status >= 500, r.RemoteAddr) {
				level := slog.LevelWarn
				if sw.status >= 500 {
					level = slog.LevelError
				}
				log.LogAttrs(r.Context(), level, "request", attrs...)
			}
		})
	}
}

// ServerErrorLog là ErrorLog cho http.Server. Lỗi bắt tay TLS ("http: TLS handshake error
// from ...") xảy ra mỗi khi bot quét cổng, kết nối đứt giữa chừng hay agent dùng sai chứng
// chỉ: ghi chi tiết lần đầu mỗi loại trong cửa sổ, còn lại gộp. Các lỗi khác ghi mức warn.
func ServerErrorLog(l *slog.Logger) *log.Logger {
	return log.New(&serverErrorWriter{log: l, agg: &errAggregator{
		log: l, msg: "lỗi bắt tay TLS (đã gộp; thường do bot quét cổng, kết nối đứt hoặc sai chứng chỉ)", key: "theo_loai",
	}}, "", 0)
}

type serverErrorWriter struct {
	log *slog.Logger
	agg *errAggregator
}

func (w *serverErrorWriter) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	// Dạng: "http: TLS handshake error from 1.2.3.4:5678: <lý do>"
	if rest, ok := strings.CutPrefix(line, "http: TLS handshake error from "); ok {
		remote, reason, _ := strings.Cut(rest, ": ")
		if w.agg.first(truncatePath(reason), false, remote) {
			w.log.Warn("lỗi bắt tay TLS", "remote", remote, "ly_do", truncatePath(reason))
		}
		return len(p), nil
	}
	w.log.Warn(truncatePath(line))
	return len(p), nil
}

// truncatePath: chuỗi do người ngoài gửi (đường dẫn, lý do lỗi), cắt bớt để một dòng
// log không dài vô hạn.
func truncatePath(p string) string {
	if len(p) > 200 {
		return p[:200] + "…"
	}
	return p
}

// errAggregator đếm lỗi theo loại trong từng cửa sổ PublicLogWindow.
type errAggregator struct {
	log *slog.Logger
	msg string // nội dung dòng gộp
	key string // tên trường chứa số lượng theo loại

	mu     sync.Mutex
	active bool
	server bool            // trong cửa sổ có lỗi phía server (5xx) → dòng gộp ở mức error
	seen   map[string]bool // loại đã ghi chi tiết trong cửa sổ này
	counts map[string]int  // số lần bị gộp theo loại
	byIP   map[string]int  // số lần bị gộp theo IP
}

// first ghi nhận một lỗi; trả true nếu là lần đầu của loại này trong cửa sổ (ghi chi tiết).
func (a *errAggregator) first(kind string, server bool, remote string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active {
		a.active, a.server = true, false
		a.seen, a.counts, a.byIP = map[string]bool{}, map[string]int{}, map[string]int{}
		time.AfterFunc(PublicLogWindow, a.flush)
	}
	if !a.seen[kind] && len(a.seen) < publicLogMaxKinds {
		a.seen[kind] = true
		return true
	}
	if _, ok := a.counts[kind]; !ok && len(a.counts) >= publicLogMaxKinds {
		kind = "khác"
	}
	a.counts[kind]++
	a.server = a.server || server
	ip, _, err := net.SplitHostPort(remote)
	if err != nil {
		ip = remote
	}
	if _, ok := a.byIP[ip]; ok || len(a.byIP) < publicLogMaxIPs {
		a.byIP[ip]++
	}
	return false
}

func (a *errAggregator) flush() {
	a.mu.Lock()
	counts, byIP, server := a.counts, a.byIP, a.server
	a.active = false
	a.mu.Unlock()

	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return
	}
	topIP, topN := "", 0
	for ip, n := range byIP {
		if n > topN {
			topIP, topN = ip, n
		}
	}
	level := slog.LevelWarn
	if server {
		level = slog.LevelError
	}
	a.log.Log(context.Background(), level, a.msg,
		"cua_so_s", int(PublicLogWindow.Seconds()), "so_lan", total, a.key, counts,
		"ip_nhieu_nhat", topIP, "so_lan_cua_ip_do", topN, "so_ip", len(byIP))
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		if n /= 10; n == 0 {
			return string(b[i:])
		}
	}
}

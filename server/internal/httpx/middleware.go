package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"
)

// Middleware bọc một handler.
type Middleware func(http.Handler) http.Handler

// Chain áp middleware theo thứ tự: phần tử đầu tiên chạy ngoài cùng.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type ctxKey int

const requestIDKey ctxKey = iota

// RequestID lấy request id từ context (rỗng nếu không có).
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithRequestID gắn X-Request-Id ngẫu nhiên cho mỗi request. Không nhận id do client
// gửi lên: phía trước server không có load balancer, nên id từ bên ngoài chỉ có thể là
// giả, dùng để làm rối việc tra log (ví dụ trùng id với một request thật).
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8)
		rand.Read(b)
		id := hex.EncodeToString(b)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap: cho http.ResponseController (Flush, SetWriteDeadline) đi xuyên lớp ghi log này — luồng
// thông báo tức thời của web quản trị cần đẩy dữ liệu xuống ngay.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// AccessLog ghi một dòng log cho mỗi request. Health check ghi ở mức debug
// để load balancer gọi liên tục không làm đầy log.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			if sw.status == 0 {
				sw.status = http.StatusOK
			}
			level := slog.LevelInfo
			switch {
			case r.URL.Path == "/healthz" || r.URL.Path == "/readyz":
				level = slog.LevelDebug
			case sw.status >= 500:
				level = slog.LevelError
			case sw.status >= 400:
				level = slog.LevelWarn
			}
			log.LogAttrs(r.Context(), level, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Int("bytes", sw.bytes),
				slog.Int64("ms", time.Since(start).Milliseconds()),
				slog.String("remote", r.RemoteAddr),
				slog.String("req_id", RequestID(r.Context())),
			)
		})
	}
}

// Recover bắt panic trong handler, ghi log kèm stack và trả 500 thay vì làm sập server.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v)
					}
					log.Error("panic trong handler", "panic", v, "path", r.URL.Path,
						"req_id", RequestID(r.Context()), "stack", string(debug.Stack()))
					WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// MaxBody giới hạn kích thước body của request. Request khai Content-Length lớn hơn
// giới hạn bị trả 413 ngay, không đọc body; body không khai độ dài (chunked) bị cắt
// khi đọc quá giới hạn.
func MaxBody(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				w.Header().Set("Connection", "close") // không đọc hết body thừa
				WriteError(w, http.StatusRequestEntityTooLarge, "body quá lớn")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// Limit cho tối đa n request của nhóm này được xử lý cùng lúc. Hết chỗ thì trả 503
// ngay (không xếp hàng, không giữ kết nối), kèm Retry-After và next_poll_s để agent
// giãn nhịp gọi. Log tối đa mỗi dropLogEvery một lần.
func Limit(name string, n int, log *slog.Logger) Middleware {
	slots := make(chan struct{}, n)
	var (
		mu      sync.Mutex
		lastLog time.Time
		busy    int
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				next.ServeHTTP(w, r)
			default:
				mu.Lock()
				busy++
				if time.Since(lastLog) >= dropLogEvery {
					log.Warn("server đang bận, trả 503 cho agent giãn nhịp", "nhom", name, "gioi_han", n, "so_request_bi_tu_choi", busy)
					lastLog, busy = time.Now(), 0
				}
				mu.Unlock()
				w.Header().Set("Retry-After", "60")
				WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
					"ok": false, "error": "server đang bận, thử lại sau", "next_poll_s": 600,
				})
			}
		})
	}
}

// apiCSP: response mặc định (JSON, lỗi) không được chạy hay tải gì. Trang giao diện
// (internal/web) tự đặt CSP riêng, ghi đè giá trị này.
const apiCSP = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// SecureHeaders gắn header bảo mật cho mọi response, kể cả JSON và trang lỗi 404/405:
// trình duyệt không đoán kiểu nội dung (nosniff), không chạy script, không cho nhúng iframe.
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", apiCSP)
		next.ServeHTTP(w, r)
	})
}

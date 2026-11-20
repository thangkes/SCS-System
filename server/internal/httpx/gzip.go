package httpx

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// GzipJSON nén phản hồi JSON / CSV (danh sách máy, cảnh báo, báo cáo…) khi trình duyệt nhận gzip — thường nhỏ
// đi 5–10 lần. Chỉ dùng cho API quản trị đọc dữ liệu: KHÔNG nén luồng SSE (đẩy ngay từng gói) và route
// đăng nhập / tài khoản (không trộn dữ liệu do người ngoài điều khiển với bí mật trong cùng phản hồi nén —
// chống BREACH). skip(r) = true thì bỏ qua.
func GzipJSON(skip func(r *http.Request) bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip(r) || !acceptsGzip(r) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Add("Vary", "Accept-Encoding")
			gw := &gzipWriter{ResponseWriter: w}
			defer gw.close()
			next.ServeHTTP(gw, r)
		})
	}
}

var gzPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression); return w }}

type gzipWriter struct {
	http.ResponseWriter
	decided bool
	gz      *gzip.Writer
}

// decide: chỉ nén phản hồi 200 có kiểu JSON / CSV, chưa tự mã hóa.
func (g *gzipWriter) decide(code int) {
	if g.decided {
		return
	}
	g.decided = true
	h := g.Header()
	ct := h.Get("Content-Type")
	if code != http.StatusOK || h.Get("Content-Encoding") != "" ||
		!(strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/csv")) {
		return
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	g.gz = gzPool.Get().(*gzip.Writer)
	g.gz.Reset(g.ResponseWriter)
}

func (g *gzipWriter) WriteHeader(code int) {
	g.decide(code)
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		g.WriteHeader(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) Flush() {
	if g.gz != nil {
		g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipWriter) close() {
	if g.gz != nil {
		g.gz.Close()
		gzPool.Put(g.gz)
		g.gz = nil
	}
}

// Unwrap cho http.ResponseController (hạn ghi, flush) đi tới ResponseWriter gốc.
func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func acceptsGzip(r *http.Request) bool {
	for _, p := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(p), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") && strings.TrimSpace(q) != "q=0" {
			return true
		}
	}
	return false
}

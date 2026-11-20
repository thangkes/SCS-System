// Package web nhúng giao diện quản trị (HTML/CSS/JS thuần) vào binary scs-server.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var files embed.FS

// CSP chỉ cho tải tài nguyên từ chính server: không script/style inline, không
// nguồn ngoài, không nhúng trang vào iframe. Giao diện được viết để chạy trong giới hạn này.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

// asset: một file giao diện, tính sẵn lúc khởi động: ETag (SHA-256 nội dung) để trình duyệt hỏi lại chỉ
// nhận 304 khi không đổi, và bản nén gzip (app.js ~237 KB → ~55 KB).
type asset struct {
	body, gz []byte
	etag     string
	ctype    string
}

var ctypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".svg":  "image/svg+xml",
}

func loadAssets(sub fs.FS) map[string]*asset {
	out := map[string]*asset{}
	err := fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		a := &asset{body: b, etag: `"` + hex.EncodeToString(sum[:12]) + `"`, ctype: ctypes[path.Ext(p)]}
		if a.ctype == "" {
			a.ctype = "application/octet-stream"
		}
		if a.ctype != "image/svg+xml" || len(b) > 1024 {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			zw.Write(b)
			zw.Close()
			if buf.Len() < len(b) {
				a.gz = buf.Bytes()
			}
		}
		out[p] = a
		return nil
	})
	if err != nil {
		panic(err) // chỉ xảy ra nếu thư mục nhúng sai lúc build
	}
	return out
}

// Mount gắn giao diện vào mux:
//
//	GET /           trang chính
//	GET /static/... CSS, JS, icon
//
// Đường dẫn không có trong danh sách trả 404 của mux (không có route "bắt tất cả").
func Mount(mux *http.ServeMux) {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // chỉ xảy ra nếu thư mục nhúng sai lúc build
	}
	assets := loadAssets(sub)
	mux.Handle("GET /{$}", secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, r, assets["index.html"])
	})))
	mux.Handle("GET /static/", secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/static/")
		a := assets[name]
		if name == "" || name == "index.html" || strings.Contains(name, "..") || a == nil {
			http.NotFound(w, r) // không liệt kê thư mục, không có đường dẫn lạ
			return
		}
		serveAsset(w, r, a)
	})))
}

// serveAsset: trả 304 khi trình duyệt đã có đúng bản (If-None-Match), gzip khi trình duyệt nhận được.
func serveAsset(w http.ResponseWriter, r *http.Request, a *asset) {
	h := w.Header()
	h.Set("ETag", a.etag)
	h.Set("Vary", "Accept-Encoding")
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", a.ctype)
	body := a.body
	if a.gz != nil && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

func etagMatch(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == etag || t == "*" {
			return true
		}
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, p := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(p), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") && strings.TrimSpace(q) != "q=0" {
			return true
		}
	}
	return false
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Luôn hỏi lại server (thấy bản mới ngay sau khi cập nhật); không đổi thì chỉ nhận 304 nhờ ETag.
		h.Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// AcceptsGzip: dùng chung cho middleware nén JSON của API quản trị.
func AcceptsGzip(r *http.Request) bool { return acceptsGzip(r) }

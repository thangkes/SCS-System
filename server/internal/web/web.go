// Package web nhúng giao diện quản trị (HTML/CSS/JS thuần) vào binary scs-server.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var files embed.FS

// CSP chỉ cho tải tài nguyên từ chính server: không script/style inline, không
// nguồn ngoài, không nhúng trang vào iframe. Giao diện được viết để chạy trong giới hạn này.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

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
	static := http.StripPrefix("/static/", http.FileServerFS(sub))

	mux.Handle("GET /{$}", secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, sub, "index.html")
	})))
	mux.Handle("GET /static/", secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/static/" {
			http.NotFound(w, r) // không liệt kê thư mục
			return
		}
		static.ServeHTTP(w, r)
	})))
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache") // luôn hỏi lại server để thấy bản mới sau khi cập nhật
		next.ServeHTTP(w, r)
	})
}

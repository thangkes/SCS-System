package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestIndex(t *testing.T) {
	rec := serve(t, "/")
	if rec.Code != 200 {
		t.Fatalf("/ = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "/static/app.js") {
		t.Error("index.html phải nạp /static/app.js")
	}
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("thiếu header %s", h)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP không được nới lỏng: %s", csp)
	}
}

func TestStaticFiles(t *testing.T) {
	for path, ctype := range map[string]string{
		"/static/app.js":      "javascript",
		"/static/app.css":     "text/css",
		"/static/favicon.svg": "image/svg+xml",
	} {
		rec := serve(t, path)
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), ctype) {
			t.Errorf("%s = %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s thiếu nosniff", path)
		}
	}
}

func TestNotFound(t *testing.T) {
	for _, p := range []string{"/static/", "/static/khong-co.js", "/static/../web.go", "/khong-co"} {
		if rec := serve(t, p); rec.Code == 200 {
			t.Errorf("%s phải không trả 200, được %d", p, rec.Code)
		}
	}
}

// Giao diện không được có script/style inline hay thuộc tính onXxx vì CSP sẽ chặn.
func TestNoInlineCode(t *testing.T) {
	body := serve(t, "/").Body.String()
	for _, bad := range []string{"<script>", "style=\"", "onclick=", "onload=", "onsubmit="} {
		if strings.Contains(body, bad) {
			t.Errorf("index.html có %q, CSP sẽ chặn", bad)
		}
	}
}

// ETag: lần sau gửi If-None-Match đúng thì nhận 304 (không tải lại); gzip khi trình duyệt nhận được.
func TestETagAndGzip(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux)
	do := func(h map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/static/app.js", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	first := do(nil)
	etag := first.Header().Get("ETag")
	if first.Code != 200 || etag == "" || first.Header().Get("Content-Encoding") != "" {
		t.Fatalf("lần đầu: %d etag=%q enc=%q", first.Code, etag, first.Header().Get("Content-Encoding"))
	}
	if rec := do(map[string]string{"If-None-Match": etag}); rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("đã có bản này: cần 304, được %d (%d byte)", rec.Code, rec.Body.Len())
	}
	if rec := do(map[string]string{"If-None-Match": `"khac"`}); rec.Code != 200 {
		t.Fatalf("ETag khác: cần 200, được %d", rec.Code)
	}
	gz := do(map[string]string{"Accept-Encoding": "gzip, deflate, br"})
	if gz.Header().Get("Content-Encoding") != "gzip" || gz.Body.Len() >= first.Body.Len()/2 || gz.Header().Get("Vary") == "" {
		t.Fatalf("gzip: enc=%q %d byte (gốc %d)", gz.Header().Get("Content-Encoding"), gz.Body.Len(), first.Body.Len())
	}
	if !strings.Contains(gz.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("Content-Type: %q", gz.Header().Get("Content-Type"))
	}
}

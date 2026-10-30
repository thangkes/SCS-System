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

package httpx

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Chỉ nén JSON / CSV trạng thái 200 khi trình duyệt nhận gzip; bỏ qua route skip, lỗi, kiểu khác.
func TestGzipJSON(t *testing.T) {
	big := strings.Repeat(`{"hostname":"12345-TN1-PRI","status":"approved"},`, 200)
	h := GzipJSON(func(r *http.Request) bool { return r.URL.Path == "/skip" })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/err":
			WriteJSON(w, 400, map[string]string{"error": big})
		case "/text":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, big)
		default:
			WriteJSON(w, 200, map[string]string{"list": big})
		}
	}))
	get := func(path string, gz bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if gz {
			req.Header.Set("Accept-Encoding", "gzip")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := get("/list", true)
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Body.Len() > len(big)/5 {
		t.Fatalf("JSON lớn phải nén: enc=%q %d byte", rec.Header().Get("Content-Encoding"), rec.Body.Len())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); !strings.Contains(string(b), "12345-TN1-PRI") {
		t.Fatal("giải nén không ra nội dung")
	}
	for _, c := range []struct {
		path string
		gz   bool
	}{{"/list", false}, {"/skip", true}, {"/err", true}, {"/text", true}} {
		if rec := get(c.path, c.gz); rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s (gzip=%v) không được nén", c.path, c.gz)
		}
	}
}

package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 1000 request 404 + 1 request 401 dồn dập: chỉ ghi chi tiết lần đầu mỗi mã, phần còn lại
// gộp thành một dòng thống kê cuối cửa sổ. Trước khi sửa: 1001 dòng log.
func TestPublicAccessLogAggregates(t *testing.T) {
	old := PublicLogWindow
	PublicLogWindow = 200 * time.Millisecond
	t.Cleanup(func() { PublicLogWindow = old })

	buf := &lockedBuffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /auth", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	h := PublicAccessLog(log)(mux)

	send := func(path, remote string) {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = remote
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	for i := 0; i < 1000; i++ {
		send("/khong-co/"+strings.Repeat("x", i%300), "203.0.113.9:4000")
	}
	send("/auth", "198.51.100.1:5000")
	send("/ok", "198.51.100.1:5000")

	if !buf.waitFor("đã gộp") {
		t.Fatal("thiếu dòng gộp cuối cửa sổ")
	}
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		json.Unmarshal([]byte(l), &m)
		lines = append(lines, m)
	}
	warn, debug, summary := 0, 0, map[string]any(nil)
	for _, m := range lines {
		switch {
		case m["msg"] == "request" && m["level"] == "WARN":
			warn++
			if p, _ := m["path"].(string); len(p) > 210 {
				t.Errorf("đường dẫn trong log phải bị cắt ngắn: %d ký tự", len(p))
			}
		case m["msg"] == "request" && m["level"] == "DEBUG":
			debug++
		case strings.Contains(m["msg"].(string), "đã gộp"):
			summary = m
		}
	}
	if warn != 2 || debug != 1 {
		t.Fatalf("chi tiết: %d dòng WARN (muốn 2: một 404, một 401), %d DEBUG (muốn 1)", warn, debug)
	}
	if summary["so_lan"] != float64(999) || summary["ip_nhieu_nhat"] != "203.0.113.9" {
		t.Fatalf("dòng gộp sai: %v", summary)
	}
	if len(lines) != 4 {
		t.Fatalf("tổng cộng phải 4 dòng log cho 1002 request, được %d", len(lines))
	}

	// Cửa sổ mới: lỗi lại được ghi chi tiết lần đầu.
	send("/khong-co", "203.0.113.9:4000")
	if n := strings.Count(buf.String(), `"level":"WARN","msg":"request"`); n != 3 {
		t.Fatalf("cửa sổ mới phải ghi chi tiết lại, đếm được %d", n)
	}
}

// 300 kết nối rác vào cổng TLS (bot quét cổng): log chỉ có vài dòng chi tiết + một dòng gộp.
// Trước khi sửa: mỗi kết nối một dòng "TLS handshake error".
func TestTLSHandshakeErrorsAggregated(t *testing.T) {
	old := PublicLogWindow
	PublicLogWindow = 300 * time.Millisecond
	t.Cleanup(func() { PublicLogWindow = old })

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	cfg, _, err := SelfSignedTLS()
	if err != nil {
		t.Fatal(err)
	}
	buf := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go (&Server{Name: "t", Addr: addr, TLS: cfg, Handler: http.NotFoundHandler(),
		Log: slog.New(slog.NewJSONHandler(buf, nil))}).Run(ctx)
	time.Sleep(200 * time.Millisecond)

	for i := 0; i < 300; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")) // HTTP thường vào cổng HTTPS
		} else {
			c.Write([]byte("\x16\x03\x01rac"))
		}
		c.SetReadDeadline(time.Now().Add(time.Second))
		io.Copy(io.Discard, c)
		c.Close()
	}
	if !buf.waitFor("lỗi bắt tay TLS (đã gộp") {
		t.Fatalf("thiếu dòng gộp; log:\n%s", buf.String())
	}
	lines := strings.Count(buf.String(), "\n")
	detail := strings.Count(buf.String(), `"msg":"lỗi bắt tay TLS"`)
	if detail < 1 || detail > 5 || lines > 10 {
		t.Fatalf("300 kết nối rác ra %d dòng log (%d dòng chi tiết), muốn vài dòng", lines, detail)
	}
	if strings.Contains(buf.String(), "TLS handshake error") {
		t.Error("dòng thô của http.Server không được lọt ra")
	}
}

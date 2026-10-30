package httpx

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLimitRejectsWhenFull(t *testing.T) {
	buf := &lockedBuffer{}
	release := make(chan struct{})
	var started sync.WaitGroup
	h := Limit("thu", 3, slog.New(slog.NewTextHandler(buf, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Done()
		<-release
	}))
	var done sync.WaitGroup
	for i := 0; i < 3; i++ {
		started.Add(1)
		done.Add(1)
		go func() { defer done.Done(); h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil)) }()
	}
	started.Wait() // 3 chỗ đã đầy
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" ||
			!strings.Contains(rec.Body.String(), `"next_poll_s":600`) {
			t.Fatalf("hết chỗ phải 503 + Retry-After + next_poll_s: %d %v %s", rec.Code, rec.Header(), rec.Body)
		}
	}
	if n := strings.Count(buf.String(), "đang bận"); n != 1 {
		t.Fatalf("log bận phải giới hạn tần suất, được %d dòng", n)
	}
	close(release)
	done.Wait()
	rec := httptest.NewRecorder()
	h2 := Limit("thu", 1, slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	h2.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
	h2.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("xử lý xong phải trả chỗ: %d", rec.Code)
	}
}

func TestMaxBodyRejectsDeclaredLength(t *testing.T) {
	called := false
	h := MaxBody(100)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 101)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge || called {
		t.Fatalf("Content-Length vượt giới hạn phải 413 ngay, không gọi handler: %d called=%v", rec.Code, called)
	}
}

// Server bật DisableHTTP2 chỉ đàm phán HTTP/1.1 (trước khi sửa: cổng agent chọn "h2").
func TestDisableHTTP2(t *testing.T) {
	for _, disable := range []bool{true, false} {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := l.Addr().String()
		l.Close()
		cfg, _, err := SelfSignedTLS()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		s := &Server{Name: "t", Addr: addr, TLS: cfg, DisableHTTP2: disable,
			Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			Log:     slog.New(slog.NewTextHandler(io.Discard, nil))}
		go s.Run(ctx)
		var c *tls.Conn
		for i := 0; i < 100; i++ {
			if c, err = tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}}); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		got := c.ConnectionState().NegotiatedProtocol
		if disable && got == "h2" || !disable && got != "h2" {
			t.Errorf("DisableHTTP2=%v nhưng ALPN chọn %q", disable, got)
		}
		if disable { // HTTP/1.1 vẫn chạy bình thường
			c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
			if resp, err := http.ReadResponse(bufio.NewReader(c), nil); err != nil || resp.StatusCode != 200 {
				t.Errorf("HTTP/1.1: %v", err)
			}
		}
		c.Close()
		cancel()
	}
}

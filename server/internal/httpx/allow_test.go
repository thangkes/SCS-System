package httpx

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer: log được ghi từ goroutine accept trong khi test đọc.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// waitFor chờ tối đa 2 giây cho tới khi log chứa đoạn text (log ghi sau khi đóng kết nối).
func (l *lockedBuffer) waitFor(text string) bool {
	for i := 0; i < 200; i++ {
		if strings.Contains(l.String(), text) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func serveWithAllow(t *testing.T, allow []netip.Prefix) (string, *lockedBuffer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	al := newAllowListener(ln, allow, slog.New(slog.NewTextHandler(logs, nil)))
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })}
	go srv.Serve(al)
	t.Cleanup(func() { srv.Close() })
	return "http://" + ln.Addr().String() + "/", logs
}

func fetch(url string) (string, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func TestAllowListenerBlocks(t *testing.T) {
	url, logs := serveWithAllow(t, []netip.Prefix{netip.MustParsePrefix("10.8.0.0/24")})
	if body, err := fetch(url); err == nil {
		t.Fatalf("IP ngoài danh sách phải bị đóng kết nối, lại nhận được %q", body)
	}
	if !logs.waitFor("chặn kết nối") {
		t.Fatal("phải ghi log khi chặn")
	}
	// Bị dò liên tục chỉ ghi log một lần trong khoảng dropLogEvery.
	for i := 0; i < 5; i++ {
		fetch(url)
	}
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(logs.String(), "chặn kết nối"); n != 1 {
		t.Fatalf("ghi log %d lần, muốn 1", n)
	}
}

func TestAllowListenerAllows(t *testing.T) {
	url, _ := serveWithAllow(t, []netip.Prefix{netip.MustParsePrefix("10.8.0.0/24"), netip.MustParsePrefix("127.0.0.0/8")})
	if body, err := fetch(url); err != nil || body != "ok" {
		t.Fatalf("IP trong danh sách phải vào được: body=%q err=%v", body, err)
	}
}

func TestAllowedMatching(t *testing.T) {
	list := []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("192.168.1.5/32")}
	cases := map[string]bool{
		"100.64.0.10": true,  // Tailscale
		"100.128.0.1": false, // ngoài /10
		"::1":         true,
		"192.168.1.5": true,
		"192.168.1.6": false,
		"8.8.8.8":     false,
	}
	for s, want := range cases {
		if got := allowed(list, netip.MustParseAddr(s)); got != want {
			t.Errorf("%s: allowed=%v, muốn %v", s, got, want)
		}
	}
	// IPv4 đi qua socket IPv6 (::ffff:a.b.c.d) phải được quy về IPv4.
	ip, ok := remoteIP(&net.TCPAddr{IP: net.ParseIP("::ffff:192.168.1.5"), Port: 1})
	if !ok || !allowed(list, ip) {
		t.Errorf("::ffff:192.168.1.5 phải khớp 192.168.1.5/32, được %v", ip)
	}
}

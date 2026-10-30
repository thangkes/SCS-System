package httpx

import (
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// dialN mở n kết nối TCP và trả về các kết nối còn sống sau khi server xử lý accept.
func dialN(t *testing.T, addr string, n int) (alive, closed []net.Conn) {
	t.Helper()
	for i := 0; i < n; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		// Server đóng kết nối bị từ chối ngay; đọc thử để biết.
		c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); err != nil && isTimeout(err) {
			alive = append(alive, c)
		} else {
			closed = append(closed, c)
		}
	}
	return alive, closed
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

// serveHold nhận kết nối qua listener giới hạn và giữ nguyên (như kết nối keep-alive nhàn rỗi).
func serveHold(t *testing.T, max, perIP int) (net.Listener, *lockedBuffer) {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	buf := &lockedBuffer{}
	ln := newConnLimitListener(inner, max, perIP, slog.New(slog.NewTextHandler(buf, nil)))
	go func() {
		for {
			if _, err := ln.Accept(); err != nil {
				return
			}
		}
	}()
	return ln, buf
}

func TestConnLimitPerIP(t *testing.T) {
	ln, buf := serveHold(t, 100, 5)
	defer ln.Close()
	alive, closed := dialN(t, ln.Addr().String(), 8)
	if len(alive) != 5 || len(closed) != 3 {
		t.Fatalf("giới hạn 5 kết nối/IP: còn %d, bị đóng %d", len(alive), len(closed))
	}
	if !buf.waitFor("một IP") {
		t.Fatal("phải ghi log khi từ chối")
	}
	if n := strings.Count(buf.String(), "từ chối kết nối"); n != 1 {
		t.Fatalf("log từ chối phải giới hạn tần suất, được %d dòng", n)
	}
	for _, c := range alive {
		c.Close()
	}
}

func TestConnLimitTotalAndRelease(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newConnLimitListener(inner, 3, 3, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer ln.Close()
	accepted := make(chan net.Conn, 10)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	alive, closed := dialN(t, ln.Addr().String(), 4)
	if len(alive) != 3 || len(closed) != 1 {
		t.Fatalf("giới hạn tổng 3: còn %d, bị đóng %d", len(alive), len(closed))
	}
	// Server đóng một kết nối (đóng hai lần cũng chỉ trả một chỗ) → có chỗ cho kết nối mới.
	c := <-accepted
	c.Close()
	c.Close()
	if alive, _ := dialN(t, ln.Addr().String(), 2); len(alive) != 1 {
		t.Fatalf("đóng 1 kết nối thì nhận thêm đúng 1, được %d", len(alive))
	}
}

func TestLimitKeyGroupsIPv6(t *testing.T) {
	a := limitKey(netip.MustParseAddr("2001:db8:1:2:aaaa::1"))
	b := limitKey(netip.MustParseAddr("2001:db8:1:2:ffff::9"))
	c := limitKey(netip.MustParseAddr("2001:db8:1:3::1"))
	if a != b || a == c {
		t.Fatalf("IPv6 phải gộp theo /64: %v %v %v", a, b, c)
	}
	if v4 := netip.MustParseAddr("203.0.113.7"); limitKey(v4) != v4 {
		t.Fatal("IPv4 giữ nguyên")
	}
}

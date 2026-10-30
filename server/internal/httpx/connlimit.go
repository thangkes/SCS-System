package httpx

import (
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// connLimitListener giới hạn số kết nối đang mở: tổng cộng và theo từng IP nguồn.
// Kết nối vượt giới hạn bị đóng ngay sau accept, trước khi bắt tay TLS, nên kẻ tấn
// công mở hàng chục nghìn kết nối rồi để yên cũng không chiếm hết RAM hay file descriptor.
//
// Giới hạn theo IP phải đủ rộng cho một cửa hàng có nhiều máy sau cùng một IP WAN (NAT).
type connLimitListener struct {
	net.Listener
	max, perIP int
	log        *slog.Logger

	mu      sync.Mutex
	total   int
	byIP    map[netip.Addr]int
	lastLog time.Time
	dropped int
}

func newConnLimitListener(ln net.Listener, max, perIP int, log *slog.Logger) net.Listener {
	return &connLimitListener{Listener: ln, max: max, perIP: perIP, log: log, byIP: map[netip.Addr]int{}}
}

func (l *connLimitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip, _ := remoteIP(c.RemoteAddr())
		ip = limitKey(ip)
		if l.acquire(ip) {
			return &limitedConn{Conn: c, release: func() { l.release(ip) }}, nil
		}
		c.Close()
	}
}

func (l *connLimitListener) acquire(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	reason := ""
	switch {
	case l.max > 0 && l.total >= l.max:
		reason = "tổng"
	case l.perIP > 0 && l.byIP[ip] >= l.perIP:
		reason = "một IP"
	default:
		l.total++
		l.byIP[ip]++
		return true
	}
	// Ghi log tối đa mỗi dropLogEvery một lần, kèm số kết nối đã từ chối.
	l.dropped++
	if time.Since(l.lastLog) >= dropLogEvery {
		l.log.Warn("từ chối kết nối: vượt giới hạn số kết nối đang mở (có thể đang bị tấn công)",
			"gioi_han", reason, "remote", ip.String(), "dang_mo", l.total, "cua_ip_nay", l.byIP[ip],
			"so_ket_noi_bi_tu_choi", l.dropped)
		l.lastLog, l.dropped = time.Now(), 0
	}
	return false
}

func (l *connLimitListener) release(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.byIP[ip]--; l.byIP[ip] <= 0 {
		delete(l.byIP, ip)
	}
}

// limitKey: IPv4 tính theo từng địa chỉ; IPv6 gộp theo dải /64 vì một máy (hoặc kẻ tấn
// công) thường được cấp cả dải /64 và đổi địa chỉ trong dải tùy ý.
func limitKey(ip netip.Addr) netip.Addr {
	if ip.Is6() {
		return netip.PrefixFrom(ip.WithZone(""), 64).Masked().Addr()
	}
	return ip
}

// limitedConn trả lại chỗ đúng một lần khi đóng (Close có thể bị gọi nhiều lần).
type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

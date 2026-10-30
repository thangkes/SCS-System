package httpx

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// allowListener chỉ nhận kết nối TCP từ các dải IP cho phép. Kết nối khác bị
// đóng ngay sau khi accept, trước khi bắt tay TLS: không tốn CPU cho TLS và
// bên ngoài không thấy được chứng chỉ hay nội dung trang.
type allowListener struct {
	net.Listener
	allow []netip.Prefix
	log   *slog.Logger

	mu      sync.Mutex
	lastLog time.Time
	dropped int
}

// Ghi log bị chặn tối đa một lần mỗi khoảng này, kèm số kết nối đã chặn, để
// bị dò quét liên tục cũng không làm đầy log.
const dropLogEvery = 30 * time.Second

func newAllowListener(ln net.Listener, allow []netip.Prefix, log *slog.Logger) net.Listener {
	return &allowListener{Listener: ln, allow: allow, log: log}
}

func (l *allowListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip, ok := remoteIP(c.RemoteAddr())
		if ok && allowed(l.allow, ip) {
			return c, nil
		}
		c.Close()
		l.noteDrop(ip)
	}
}

func (l *allowListener) noteDrop(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dropped++
	if time.Since(l.lastLog) < dropLogEvery {
		return
	}
	l.log.Warn("chặn kết nối từ IP ngoài danh sách cho phép (SCS_ADMIN_ALLOW)",
		"remote", ip.String(), "so_ket_noi_bi_chan", l.dropped)
	l.lastLog = time.Now()
	l.dropped = 0
}

func remoteIP(a net.Addr) (netip.Addr, bool) {
	if ta, ok := a.(*net.TCPAddr); ok {
		return ta.AddrPort().Addr().Unmap(), true
	}
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

func allowed(list []netip.Prefix, ip netip.Addr) bool {
	ip = ip.WithZone("")
	for _, p := range list {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// proxyHeaders là các header reverse proxy (nginx, Caddy, HAProxy...) thường thêm vào.
var proxyHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-Ip", "X-Client-Ip"}

// RejectProxied chặn request đi qua reverse proxy. Danh sách IP cho phép xét theo IP
// nguồn của kết nối TCP: đặt proxy trên chính máy server thì mọi request đều mang IP
// 127.0.0.1 (nằm trong danh sách mặc định), allowlist mất tác dụng. Server không hỗ trợ
// chạy sau proxy nên thấy header của proxy là từ chối thay vì âm thầm cho qua.
func RejectProxied(log *slog.Logger) Middleware {
	var lastLog atomic.Int64
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, h := range proxyHeaders {
				if r.Header.Get(h) == "" {
					continue
				}
				if now, prev := time.Now().Unix(), lastLog.Load(); now-prev >= 30 && lastLog.CompareAndSwap(prev, now) {
					log.Warn("chặn request đi qua reverse proxy: web quản trị không hỗ trợ chạy sau proxy "+
						"(proxy trên máy server làm mọi request thành 127.0.0.1, vượt qua SCS_ADMIN_ALLOW)",
						"header", h, "remote", r.RemoteAddr)
				}
				WriteError(w, http.StatusForbidden, "web quản trị không nhận request qua proxy; truy cập trực tiếp qua VPN")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

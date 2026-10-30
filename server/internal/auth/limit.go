package auth

import (
	"sync"
	"time"
)

// Giới hạn thử sai (giống NASfone):
//   - mỗi IP: 10 lần sai trong 15 phút
//   - toàn hệ thống: 20 lần sai trong 5 phút → khóa đăng nhập với mọi IP: 5 phút, bị khóa
//     lại liên tiếp thì gấp đôi (10, 20, 40, tối đa 60 phút); yên 1 giờ thì về lại 5 phút.
//     Admin mở khóa ngay được bằng `scs-server login-code -unlock` trên máy server.
const (
	ipMaxFails     = 10
	ipWindow       = 15 * time.Minute
	globalMaxFails = 20
	globalWindow   = 5 * time.Minute
	globalLockFor  = 5 * time.Minute
	globalLockMax  = 60 * time.Minute
	lockQuiet      = time.Hour // hết khóa yên bấy lâu thì thời gian khóa về mức đầu
	maxTrackedIPs  = 10000     // chặn việc bị dò từ quá nhiều IP làm phình bộ nhớ
)

// limiter đếm lần thử theo kiểu "đặt chỗ": mỗi lần thử được tính là một lần sai
// ngay khi request vào (trước khi đọc body và so mã), đăng nhập đúng thì trả lại.
// Nhờ vậy gửi hàng trăm request cùng lúc cũng chỉ có tối đa 10 request/IP được so mã.
type limiter struct {
	mu         sync.Mutex
	now        func() time.Time
	ip         map[string][]time.Time
	global     []time.Time
	lockedTill time.Time
	lockFor    time.Duration // thời gian của lần khóa gần nhất (0 = chưa khóa)
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, ip: map[string][]time.Time{}}
}

// attempt là kết quả đặt chỗ một lần thử.
type attempt struct {
	ok      bool          // được thử
	wait    time.Duration // không được thử: phải chờ bấy lâu
	left    int           // nếu lần này sai thì IP còn được thử bấy nhiêu lần
	locked  bool          // lần đặt chỗ này vừa làm khóa toàn hệ thống
	lockFor time.Duration // khóa bao lâu (khi locked)
}

// begin đặt chỗ một lần thử cho IP: tính luôn là một lần sai của IP và toàn hệ thống.
func (l *limiter) begin(ip string) attempt {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Before(l.lockedTill) {
		return attempt{wait: l.lockedTill.Sub(now)}
	}
	if _, ok := l.ip[ip]; !ok && len(l.ip) >= maxTrackedIPs {
		l.gc(now)
	}
	fails := prune(l.ip[ip], now, ipWindow)
	if len(fails) >= ipMaxFails {
		l.ip[ip] = fails
		return attempt{wait: fails[0].Add(ipWindow).Sub(now)}
	}
	l.ip[ip] = append(fails, now)
	l.global = append(prune(l.global, now, globalWindow), now)
	a := attempt{ok: true, left: ipMaxFails - len(l.ip[ip])}
	if len(l.global) >= globalMaxFails {
		// Bị khóa lại không lâu sau lần trước → gấp đôi: kẻ dò mã chỉ được ~20 lần
		// mỗi lần khóa, càng dò càng chậm thay vì đều đặn 20 lần / 5 phút.
		switch {
		case l.lockFor == 0 || now.Sub(l.lockedTill) >= lockQuiet:
			l.lockFor = globalLockFor
		default:
			l.lockFor = min(2*l.lockFor, globalLockMax)
		}
		l.lockedTill = now.Add(l.lockFor)
		l.global = nil
		a.locked, a.lockFor = true, l.lockFor
	}
	return a
}

// lockedUntil trả thời điểm hết khóa toàn hệ thống (zero = không khóa).
func (l *limiter) lockedUntil() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.now().Before(l.lockedTill) {
		return l.lockedTill
	}
	return time.Time{}
}

// unlock mở khóa ngay và xóa mọi lần sai đã đếm (cả theo IP), đưa thời gian khóa về mức đầu.
func (l *limiter) unlock() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lockedTill, l.lockFor, l.global = time.Time{}, 0, nil
	l.ip = map[string][]time.Time{}
}

// refund trả lại lần đặt chỗ gần nhất của IP (đăng nhập đúng, hoặc request hỏng
// chưa hề so mã).
func (l *limiter) refund(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if v := l.ip[ip]; len(v) > 0 {
		l.ip[ip] = v[:len(v)-1]
	}
	if len(l.global) > 0 {
		l.global = l.global[:len(l.global)-1]
	}
}

// gc bỏ các IP không còn lần sai nào trong cửa sổ.
func (l *limiter) gc(now time.Time) {
	for k, v := range l.ip {
		if v = prune(v, now, ipWindow); len(v) == 0 {
			delete(l.ip, k)
		} else {
			l.ip[k] = v
		}
	}
}

func prune(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= window {
		i++
	}
	return ts[i:]
}

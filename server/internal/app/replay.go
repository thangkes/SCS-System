package app

import (
	"sync"
	"time"
)

// Chống gửi lại gói (R3). Mọi gói agent có nonce ngẫu nhiên 16 byte và giờ gửi; server đã từ chối gói lệch giờ
// quá maxClockSkew. Trong cửa sổ đó, server nhớ (khóa của bên gửi, nonce) của mọi gói ĐÃ KIỂM CHỮ KÝ và từ
// chối gói trùng — kẻ bắt được một gói (đã khó vì TLS) không gửi lại được: không lặp được heartbeat / lô sự kiện,
// không lặp được gói gia hạn (lặp gói gia hạn sẽ làm server cấp chứng chỉ mới, chứng chỉ máy đang dùng mất hiệu lực).
//
// Chỉ ghi sau khi kiểm chữ ký nên người ngoài không làm đầy được bộ nhớ. Bộ nhớ trong RAM: khởi động lại server
// thì quên (kẻ tấn công còn tối đa maxClockSkew để gửi lại gói bắt được trước lúc khởi động lại — chấp nhận).

const (
	replayKeep = 2*maxClockSkew + time.Minute // nhớ tới khi giờ trong gói chắc chắn đã ngoài cửa sổ hợp lệ
	replayMax  = 2_000_000                    // trần số mục (≈ vài trăm MB tối đa ở quy mô rất lớn)
)

type replayGuard struct {
	mu     sync.Mutex
	seen   map[string]time.Time // khóa → hết nhớ lúc
	lastGC time.Time
}

var replays = &replayGuard{seen: map[string]time.Time{}}

// fresh: true nếu gói (khóa bên gửi + nonce) chưa từng thấy trong cửa sổ — và ghi nhớ nó; false = gói gửi lại.
func (g *replayGuard) fresh(sender, nonce string, now time.Time) bool {
	key := sender + "\x00" + nonce
	g.mu.Lock()
	defer g.mu.Unlock()
	if until, ok := g.seen[key]; ok && now.Before(until) {
		return false
	}
	if now.Sub(g.lastGC) > time.Minute || len(g.seen) >= replayMax {
		for k, until := range g.seen {
			if !now.Before(until) {
				delete(g.seen, k)
			}
		}
		g.lastGC = now
	}
	if len(g.seen) >= replayMax {
		// Đầy (chỉ xảy ra khi số máy rất lớn): bỏ một nửa mục cũ nhất ước lượng — nhanh, chấp nhận nhớ ngắn hơn.
		n := 0
		for k := range g.seen {
			delete(g.seen, k)
			if n++; n >= replayMax/2 {
				break
			}
		}
	}
	g.seen[key] = now.Add(replayKeep)
	return true
}

// validNonce: nonce agent sinh (NewNonce: 16 byte hex = 32 ký tự); chấp nhận 16–64 ký tự hex.
func validNonce(n string) bool {
	if len(n) < 16 || len(n) > 64 {
		return false
	}
	for _, c := range n {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

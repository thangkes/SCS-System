package heartbeat

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Heartbeat đầu tiên sau khi MÁY KHỞI ĐỘNG được dời ngẫu nhiên 5–30 phút (theo giây).
//
// Máy tại điểm bán khởi động lại cùng giờ mỗi sáng (lịch reboot định kỳ): nếu agent gọi về ngay thì hàng
// nghìn máy dồn heartbeat (và tải bản agent / bộ rule) vào cùng một phút. Chỉ áp khi:
//   - máy vừa khởi động (uptime dưới BootWindow): agent khởi động lại giữa chừng (sau khi tự cập nhật,
//     sau khi admin restart dịch vụ, sau khi tiến trình chết) vẫn gọi ngay — bản agent mới cần báo
//     "chạy tốt" sớm;
//   - máy đã được duyệt từ trước (không phải lần chạy đầu tiên / đang đăng ký: những lần đó gọi ngay).
//
// Để không bị báo mất liên lạc oan (server báo sau 60 phút không nghe gì), khoảng chờ được cắt sao cho
// từ heartbeat thành công gần nhất (trước khi máy tắt) tới heartbeat đầu sau khi bật không quá MaxGap,
// nhưng không dưới BootDelayMin. Trong lúc chờ, phần quét log vẫn chạy và vẫn gửi sự kiện như thường.
var (
	BootWindow   = 20 * time.Minute
	BootDelayMin = 5 * time.Minute
	BootDelayMax = 30 * time.Minute
	MaxGap       = 45 * time.Minute
)

const lastFile = "last-heartbeat" // giây Unix của heartbeat thành công gần nhất (trong thư mục trạng thái)

// BootDelay: thời gian chờ trước heartbeat đầu tiên. uptime: máy đã chạy bao lâu; since: từ heartbeat
// thành công gần nhất tới giờ (haveLast = false nếu không biết). Trả 0 nếu không phải vừa khởi động.
func BootDelay(uptime, since time.Duration, haveLast bool) time.Duration {
	if uptime < 0 || uptime >= BootWindow {
		return 0
	}
	span := int64((BootDelayMax - BootDelayMin) / time.Second)
	d := BootDelayMin + time.Duration(rand.Int64N(span+1))*time.Second
	if haveLast && since >= 0 {
		if limit := MaxGap - since; d > limit {
			d = max(limit.Truncate(time.Second), BootDelayMin)
		}
	}
	return d
}

// LastHeartbeat: lúc heartbeat thành công gần nhất (ghi bởi saveLast).
func LastHeartbeat(stateDir string) (time.Time, bool) {
	b, err := os.ReadFile(filepath.Join(stateDir, lastFile))
	if err != nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

// saveLast ghi lúc heartbeat thành công (ghi file tạm rồi đổi tên; lỗi thì bỏ qua — chỉ dùng để tính chờ).
func saveLast(stateDir string, t time.Time) {
	p := filepath.Join(stateDir, lastFile)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(t.Unix(), 10)), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

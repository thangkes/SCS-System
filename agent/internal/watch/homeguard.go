package watch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

// Dọn cửa vào bằng SSH của user dịch vụ.
//
// Thư mục trạng thái (/var/lib/scs-agent) cũng là thư mục home của user scs-agent (install.sh: useradd --home-dir).
// Mật khẩu user này bị khóa và shell là nologin, nhưng sshd vẫn nhận đăng nhập bằng khóa nếu home có
// .ssh/authorized_keys — kẻ đã chiếm được agent có thể tự cài khóa rồi mở đường hầm (ssh -N -L) dùng máy làm
// bàn đạp vào LAN, nologin không chặn. Agent không bao giờ cần những file này: thấy là xóa và báo lên server.
//
// Chỉ dọn trong thư mục trạng thái của agent (không bao giờ đụng home của người chạy lệnh tay).

// homeGuardNames: file / thư mục sshd (và rsh) đọc trong home để cho đăng nhập.
var homeGuardNames = []string{".ssh", ".rhosts", ".shosts"}

// HomeGuardRule: mã sự kiện gửi lên server khi phát hiện (không cần có trong bộ rule).
const HomeGuardRule = "SCS-AGENT-SSHKEY-01"

// homeGuardEvery: tối đa một sự kiện mỗi khoảng này (dọn thì vẫn dọn mỗi lần kiểm, số lần cộng dồn).
const homeGuardEvery = 10 * time.Minute

type homeGuard struct {
	dir     string
	pending int // số lần đã dọn chưa báo
	found   []string
	lastAt  time.Time
}

// sweep xóa các cửa vào trong dir; trả đường dẫn đã xóa. Symlink thì chỉ xóa chính symlink (RemoveAll không đi
// theo symlink ở gốc).
func sweepHome(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil // chưa khai thư mục trạng thái: không đoán, không dọn thư mục hiện hành
	}
	var out []string
	var errs []error
	for _, n := range homeGuardNames {
		p := filepath.Join(dir, n)
		if _, err := os.Lstat(p); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, p)
	}
	return out, errors.Join(errs...)
}

// check: dọn; đến lúc báo thì trả sự kiện (nil nếu không có gì / chưa đến lúc báo).
func (g *homeGuard) check(now time.Time) (*protocol.LogEvent, []string, error) {
	removed, err := sweepHome(g.dir)
	if len(removed) > 0 {
		g.pending++
		for _, p := range removed {
			if !contains(g.found, p) {
				g.found = append(g.found, p)
			}
		}
	}
	if g.pending == 0 || (!g.lastAt.IsZero() && now.Sub(g.lastAt) < homeGuardEvery) {
		return nil, removed, err
	}
	ev := &protocol.LogEvent{RuleID: HomeGuardRule, Severity: "critical", Source: "scs-agent", Time: now.UTC(),
		Count: g.pending,
		Line: fmt.Sprintf("phát hiện và đã xóa %s trong thư mục của user agent (cửa đăng nhập SSH bằng khóa): "+
			"agent hoặc user scs-agent có thể đã bị chiếm, kiểm tra máy", strings.Join(g.found, ", "))}
	g.pending, g.found, g.lastAt = 0, nil, now
	return ev, removed, err
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

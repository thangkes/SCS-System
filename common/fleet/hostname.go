// Package fleet: quy ước tổ chức máy ở siêu thị (mã siêu thị, vị trí máy theo hostname).
// Dùng chung cho agent (báo hostname sai ngay lúc cài) và server (đọc khi máy đăng ký).
package fleet

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Role là vai trò của máy chính trong siêu thị.
type Role string

const (
	RolePrimary   Role = "PRI" // máy số 1
	RoleSecondary Role = "SEC" // máy số 2
	RoleNone      Role = ""    // máy số 3 trở đi
)

// Name là hostname đã đọc theo quy ước <mã siêu thị>-TN<vị trí>[-PRI|-SEC]:
//
//	12345-TN1-PRI   máy số 1 (máy chính)
//	12345-TN2-SEC   máy số 2 (máy chính thứ hai)
//	12345-TN3       máy số 3 trở đi: không có hậu tố
type Name struct {
	Store string // mã siêu thị, chỉ gồm chữ số
	TN    int    // vị trí máy, từ 1
	Role  Role
}

// Mã siêu thị 1–10 chữ số, vị trí 1–999. So khớp không phân biệt hoa thường
// (Linux hay để hostname chữ thường), kết quả luôn viết hoa.
var hostnameRe = regexp.MustCompile(`^([0-9]{1,10})-TN([1-9][0-9]{0,2})(?:-(PRI|SEC))?$`)

// ParseHostname đọc hostname theo quy ước. Sai mẫu thì trả lỗi mô tả sai ở đâu (hiện cho
// admin khi duyệt máy).
func ParseHostname(hostname string) (Name, error) {
	h := strings.ToUpper(strings.TrimSpace(hostname))
	m := hostnameRe.FindStringSubmatch(h)
	if m == nil {
		return Name{}, fmt.Errorf("hostname %q không đúng mẫu <mã siêu thị>-TN<số>[-PRI|-SEC], ví dụ 12345-TN1-PRI", hostname)
	}
	tn, _ := strconv.Atoi(m[2])
	n := Name{Store: m[1], TN: tn, Role: Role(m[3])}
	if want := expectedRole(tn); n.Role != want {
		switch want {
		case RoleNone:
			return Name{}, fmt.Errorf("hostname %q: máy TN%d trở đi không có hậu tố -PRI/-SEC (đúng: %s)", hostname, tn, n.withRole(want))
		default:
			return Name{}, fmt.Errorf("hostname %q: máy TN%d phải có hậu tố -%s (đúng: %s)", hostname, tn, want, n.withRole(want))
		}
	}
	return n, nil
}

// expectedRole: TN1 là PRI, TN2 là SEC, từ TN3 không có hậu tố.
func expectedRole(tn int) Role {
	switch tn {
	case 1:
		return RolePrimary
	case 2:
		return RoleSecondary
	}
	return RoleNone
}

func (n Name) withRole(r Role) string {
	n.Role = r
	return n.String()
}

// String trả hostname chuẩn (viết hoa), ví dụ "12345-TN1-PRI".
func (n Name) String() string {
	s := n.Store + "-TN" + strconv.Itoa(n.TN)
	if n.Role != RoleNone {
		s += "-" + string(n.Role)
	}
	return s
}

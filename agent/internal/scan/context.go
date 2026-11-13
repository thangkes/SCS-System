package scan

import (
	"bufio"
	"bytes"
	"os"
	"strings"

	"github.com/thangkes/SCS-System/common/fleet"
	"github.com/thangkes/SCS-System/common/rules"
)

// MachineContext dựng ngữ cảnh máy để chọn rule theo applies_to:
//   - role, store: từ hostname theo quy ước (-TN1-PRI → pri, -TN2-SEC → sec, TN3 trở đi → phu);
//     hostname sai quy ước thì không có hai khóa này (rule cần role / store sẽ không chạy);
//   - os: ID-VERSION_ID trong /etc/os-release, ví dụ ubuntu-22.04;
//   - labels: nhãn server gửi xuống (cấu hình agent 3 cấp), ghi đè các khóa trên.
//
// Khóa / giá trị sai dạng bị bỏ.
func MachineContext(hostname string, osRelease []byte, labels map[string]string) rules.Context {
	ctx := rules.Context{}
	if n, err := fleet.ParseHostname(hostname); err == nil {
		ctx[rules.CtxStore] = n.Store
		switch n.Role {
		case fleet.RolePrimary:
			ctx[rules.CtxRole] = "pri"
		case fleet.RoleSecondary:
			ctx[rules.CtxRole] = "sec"
		default:
			ctx[rules.CtxRole] = "phu"
		}
	}
	if v := osID(osRelease); v != "" {
		ctx[rules.CtxOS] = v
	}
	for k, v := range labels {
		if rules.ValidContextKey(k) && rules.ValidContextValue(v) {
			ctx[k] = v
		}
	}
	return ctx
}

// osID: "ubuntu-22.04" từ ID và VERSION_ID; thiếu một trong hai thì rỗng.
func osID(data []byte) string {
	var id, ver string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		v = strings.ToLower(strings.Trim(v, `"'`))
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			ver = v
		}
	}
	if id == "" || ver == "" {
		return ""
	}
	s := id + "-" + ver
	if !rules.ValidContextValue(s) {
		return ""
	}
	return s
}

// LocalContext đọc hostname và /etc/os-release của máy này rồi gọi MachineContext.
func LocalContext(labels map[string]string) rules.Context {
	host, _ := os.Hostname()
	data, _ := os.ReadFile("/etc/os-release")
	if len(data) > 64<<10 {
		data = nil
	}
	return MachineContext(host, data, labels)
}

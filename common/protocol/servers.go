package protocol

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// MaxServers: số địa chỉ server tối đa server gửi xuống / agent lưu lại.
const MaxServers = 8

// CheckServerURL: địa chỉ server phải có dạng https://<IP hoặc tên miền>:<cổng>,
// không có đường dẫn, user, query.
func CheckServerURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("%q không có dạng https://<IP>:<cổng>", s)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("%q phải ghi rõ cổng, ví dụ https://192.168.1.10:6222", s)
	}
	return nil
}

// CleanServers: bỏ địa chỉ sai dạng và trùng (so sau khi bỏ "/" cuối), giữ thứ tự, tối đa MaxServers.
func CleanServers(list []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range list {
		s = strings.TrimRight(strings.TrimSpace(s), "/")
		if s == "" || seen[s] || CheckServerURL(s) != nil {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == MaxServers {
			break
		}
	}
	return out
}

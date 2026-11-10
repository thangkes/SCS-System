//go:build unix

package scan

import (
	"os"
	"syscall"
)

// sameOwner: cài bằng sudo thì file vẫn thuộc chủ thư mục trạng thái (user scs-agent),
// để dịch vụ agent (không chạy bằng root) đọc được bộ rule.
func sameOwner(ref string, paths ...string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	fi, err := os.Stat(ref)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	for _, p := range paths {
		if err := os.Lchown(p, int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
	}
	return nil
}

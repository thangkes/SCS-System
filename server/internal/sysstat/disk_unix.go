//go:build linux || darwin || freebsd

package sysstat

import (
	"path/filepath"
	"syscall"
)

// diskUsage: dung lượng ổ chứa path (thư mục chưa có thì đo thư mục cha gần nhất có).
func diskUsage(path string) (string, uint64, uint64) {
	p, _ := filepath.Abs(path)
	for {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err == nil {
			bs := uint64(st.Bsize)
			return p, st.Blocks * bs, st.Bavail * bs
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", 0, 0
		}
		p = parent
	}
}

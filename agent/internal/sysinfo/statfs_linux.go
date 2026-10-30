//go:build linux

package sysinfo

import "syscall"

// diskUsage: tổng và phần còn trống (cho user thường dùng được) của phân vùng.
func diskUsage(p string) (total, free uint64, err error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(p, &s); err != nil {
		return 0, 0, err
	}
	return s.Blocks * uint64(s.Bsize), s.Bavail * uint64(s.Bsize), nil
}

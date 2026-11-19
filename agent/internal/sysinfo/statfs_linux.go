//go:build linux

package sysinfo

import "syscall"

// diskUsage: tổng và phần còn trống (cho user thường dùng được) của phân vùng.
func diskUsage(p string) (u usage, err error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(p, &s); err != nil {
		return u, err
	}
	return usage{total: s.Blocks * uint64(s.Bsize), free: s.Bavail * uint64(s.Bsize), inodes: s.Files, inodesFree: s.Ffree}, nil
}

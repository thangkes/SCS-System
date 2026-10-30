//go:build !linux

package sysinfo

import "errors"

// diskUsage chỉ có trên Linux (agent chỉ chạy trên Ubuntu; bản này để build/test trên máy dev).
func diskUsage(string) (uint64, uint64, error) {
	return 0, 0, errors.New("chỉ hỗ trợ Linux")
}

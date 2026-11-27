//go:build !(linux && (amd64 || arm64))

package lan

import (
	"errors"
	"net/netip"
)

func ioctlPrefixes() ([]netip.Prefix, error) {
	return nil, errors.New("không hỗ trợ đọc địa chỉ bằng ioctl trên hệ này")
}

//go:build !windows

package update

import (
	"os"
	"syscall"
)

// execBinary thay tiến trình hiện tại bằng bản cập nhật (cùng tham số, thêm ChildEnv).
// Trả về chỉ khi exec thất bại.
func execBinaryOS(path string) error {
	return syscall.Exec(path, os.Args, append(os.Environ(), ChildEnv+"=1"))
}

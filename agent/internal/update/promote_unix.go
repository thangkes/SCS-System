//go:build unix

package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// ownedByRootOS: p thuộc root và nhóm / người khác không ghi được.
func ownedByRootOS(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s là symlink", p)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return fmt.Errorf("%s không thuộc root", p)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s cho nhóm / người khác ghi (quyền %o)", p, fi.Mode().Perm())
	}
	return nil
}

// readRegularOS đọc file thường, không đi theo symlink, tối đa max byte. Thư mục trạng thái do
// agent ghi, nên tác vụ root không được để bị dẫn sang đọc file khác qua symlink.
func readRegularOS(p string, max int64) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("không phải file thường")
	}
	if fi.Size() > max {
		return nil, errors.New("file quá lớn")
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}

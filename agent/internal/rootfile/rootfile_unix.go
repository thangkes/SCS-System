//go:build unix

package rootfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

func readRoot(p string, max int64) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errors.New("là symlink")
		}
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
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return nil, errors.New("không thuộc root")
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("nhóm / người khác ghi được (quyền %o)", fi.Mode().Perm())
	}
	if fi.Size() > max {
		return nil, errors.New("file quá lớn")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(b)) > max {
		return nil, errors.New("file quá lớn")
	}
	return b, err
}

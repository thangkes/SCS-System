//go:build !unix

package update

import (
	"errors"
	"io"
	"os"
)

// Thay bản khung chỉ dùng trên Linux; các hàm này để build và test được trên hệ khác.
func ownedByRootOS(string) error { return nil }

func readRegularOS(p string, max int64) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("không phải file thường")
	}
	if fi.Size() > max {
		return nil, errors.New("file quá lớn")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max+1))
}

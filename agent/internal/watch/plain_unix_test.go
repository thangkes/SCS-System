//go:build unix

package watch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlainReadable(t *testing.T) {
	stat := func(p string) os.FileInfo {
		f, err := os.Open(p)
		if err != nil {
			t.Skipf("không mở được %s: %v", p, err)
		}
		defer f.Close()
		fi, _ := f.Stat()
		return fi
	}
	own := filepath.Join(t.TempDir(), "a.log")
	os.WriteFile(own, []byte("x\n"), 0o600)
	if !plainReadable(stat(own)) {
		t.Fatal("file của chính mình (600) phải đọc được")
	}
	if !plainReadable(stat("/etc/passwd")) {
		t.Fatal("/etc/passwd (644) phải đọc được")
	}
	if os.Geteuid() != 0 {
		os.Chmod(own, 0o200) // chủ không có quyền đọc
		fi, _ := os.Stat(own)
		if plainReadable(fi) {
			t.Fatal("chủ không có quyền r thì không được tính là đọc được")
		}
	}
}

package rootfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRequired(t *testing.T) {
	for p, want := range map[string]bool{
		"/etc/scs-agent/agent.env":        true,
		"/etc/scs-agent/server-ca.pem":    true,
		"/opt/scs-agent/release.pub":      true,
		"/etc/../etc/scs-agent/agent.env": true,
		"/var/lib/scs-agent/x":            false,
		"/home/u/ca.pem":                  false,
		"ca.pem":                          false,
		"/etcx/a":                         false,
		"/dev/null":                       false,
	} {
		if got := Required(p); got != want {
			t.Errorf("Required(%q) = %v, muốn %v", p, got, want)
		}
	}
}

// File ngoài vị trí hệ thống: đọc như thường (thử trước khi cài, test).
func TestReadOutsideSystem(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a")
	os.WriteFile(p, []byte("x"), 0o666)
	if b, err := Read(p); err != nil || string(b) != "x" {
		t.Fatalf("Read = %q, %v", b, err)
	}
}

// readRoot: file của user thường, symlink, file nhóm ghi được đều bị từ chối; file của root thì được.
func TestReadRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("không có chủ root trên Windows")
	}
	dir := t.TempDir()
	own := filepath.Join(dir, "own")
	os.WriteFile(own, []byte("x"), 0o644)
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/etc/passwd", link); err != nil {
		t.Fatal(err)
	}
	if _, err := readRoot(link, MaxSize); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink tới file root phải bị từ chối, được %v", err)
	}
	if os.Geteuid() != 0 {
		if _, err := readRoot(own, MaxSize); err == nil || !strings.Contains(err.Error(), "root") {
			t.Errorf("file của user thường phải bị từ chối, được %v", err)
		}
	} else {
		os.Chmod(own, 0o664)
		if _, err := readRoot(own, MaxSize); err == nil || !strings.Contains(err.Error(), "ghi được") {
			t.Errorf("file nhóm ghi được phải bị từ chối, được %v", err)
		}
		os.Chmod(own, 0o640)
		if _, err := readRoot(own, MaxSize); err != nil {
			t.Errorf("file root 640 phải đọc được: %v", err)
		}
		if _, err := readRoot(own, 0); err == nil {
			t.Error("quá MaxSize phải bị từ chối")
		}
	}
	if _, err := readRoot("/etc/passwd", MaxSize); err != nil {
		t.Errorf("/etc/passwd (root, 644) phải đọc được: %v", err)
	}
	if _, err := readRoot(dir, MaxSize); err == nil {
		t.Error("thư mục phải bị từ chối")
	}
}

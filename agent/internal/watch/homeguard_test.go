package watch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHomeGuard(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "keep")
	os.WriteFile(outside, []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(dir, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(dir, ".ssh", "authorized_keys"), []byte("ssh-ed25519 AAAA"), 0o600)
	os.WriteFile(filepath.Join(dir, ".rhosts"), []byte("+ +"), 0o600)
	if runtime.GOOS != "windows" {
		os.Symlink(outside, filepath.Join(dir, ".shosts"))
	}
	os.WriteFile(filepath.Join(dir, "lan.json"), []byte("{}"), 0o600) // file của agent: giữ

	g := &homeGuard{dir: dir}
	t0 := time.Date(2026, 11, 29, 8, 0, 0, 0, time.UTC)
	ev, removed, err := g.check(t0)
	if err != nil || ev == nil || len(removed) < 2 {
		t.Fatalf("lần đầu phải dọn và báo: ev=%v removed=%v err=%v", ev, removed, err)
	}
	if ev.RuleID != HomeGuardRule || ev.Severity != "critical" || !strings.Contains(ev.Line, ".ssh") {
		t.Fatalf("sự kiện sai: %+v", ev)
	}
	for _, n := range homeGuardNames {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			t.Errorf("%s chưa bị xóa", n)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("symlink trỏ ra ngoài: phải chỉ xóa symlink, không xóa file đích")
	}
	if _, err := os.Stat(filepath.Join(dir, "lan.json")); err != nil {
		t.Fatal("xóa nhầm file của agent")
	}
	// Không còn gì: không báo.
	if ev, _, _ := g.check(t0.Add(time.Minute)); ev != nil {
		t.Fatal("không có gì mà vẫn báo")
	}
	// Cài lại ngay: vẫn dọn, nhưng gom báo sau homeGuardEvery.
	os.MkdirAll(filepath.Join(dir, ".ssh"), 0o700)
	if ev, removed, _ := g.check(t0.Add(2 * time.Minute)); ev != nil || len(removed) != 1 {
		t.Fatalf("trong 10 phút: dọn mà chưa báo, được ev=%v removed=%v", ev, removed)
	}
	os.MkdirAll(filepath.Join(dir, ".ssh"), 0o700)
	ev, _, _ = g.check(t0.Add(11 * time.Minute))
	if ev == nil || ev.Count != 2 {
		t.Fatalf("sau 10 phút phải báo gộp 2 lần, được %+v", ev)
	}
	// Thư mục trạng thái rỗng: không dọn thư mục hiện hành.
	if r, err := sweepHome(""); r != nil || err != nil {
		t.Fatal("dir rỗng phải bỏ qua")
	}
}

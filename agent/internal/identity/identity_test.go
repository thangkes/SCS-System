package identity

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestKeyCreatedOnceAndReused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "etc-scs-agent")
	if _, err := LoadKey(dir); !errors.Is(err, ErrNoKey) {
		t.Fatalf("chưa có khóa phải trả ErrNoKey, được %v", err)
	}
	k1, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !k1.Private.Equal(k2.Private) || k1.ID() != k2.ID() || len(k1.ID()) != 16 {
		t.Fatal("chạy lại phải dùng đúng khóa cũ, không sinh khóa mới")
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(k1.Path)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("khóa phải quyền 600, đang %o", st.Mode().Perm())
		}
	}
	// Không để lại file tạm.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("thư mục khóa có %d file, muốn 1", len(entries))
	}
}

func TestKeyRejectsOpenPermissionsAndGarbage(t *testing.T) {
	dir := t.TempDir()
	k, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		os.Chmod(k.Path, 0o644)
		if _, err := LoadKey(dir); err == nil || !strings.Contains(err.Error(), "600") {
			t.Fatalf("khóa quyền 644 phải bị từ chối: %v", err)
		}
		os.Chmod(k.Path, 0o600)
	}
	os.WriteFile(k.Path, []byte("khong phai khoa"), 0o600)
	if _, err := LoadKey(dir); err == nil {
		t.Fatal("file khóa hỏng phải báo lỗi, không được tự sinh khóa mới đè lên")
	}
}

func TestState(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadState(dir)
	if err != nil || s.Status != StatusNew {
		t.Fatalf("máy mới = %+v, %v", s, err)
	}
	want := State{Status: StatusPending, RequestedAt: time.Now().UTC().Truncate(time.Second)}
	if err := SaveState(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(dir)
	if err != nil || got.Status != want.Status || !got.RequestedAt.Equal(want.RequestedAt) {
		t.Fatalf("đọc lại = %+v, %v", got, err)
	}
	os.WriteFile(filepath.Join(dir, stateFile), []byte(`{"status":"hacked"}`), 0o600)
	if _, err := LoadState(dir); err == nil {
		t.Fatal("trạng thái lạ phải báo lỗi")
	}
}

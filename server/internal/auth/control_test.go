package auth

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestControlSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "scsc") // đường dẫn ngắn: Unix socket giới hạn ~100 ký tự
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "c.sock")

	m := NewManager("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	codes := m.Codes
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeControl(ctx, path, m, slog.New(slog.NewTextHandler(io.Discard, nil))) }()

	var got Current
	for i := 0; i < 100; i++ { // chờ socket mở
		if got, err = FetchCodes(path); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("không đọc được mã qua socket: %v", err)
	}
	if want := codes.Snapshot(); got.Admin != want.Admin || got.Viewer != want.Viewer {
		t.Fatalf("mã qua socket %+v khác mã thật %+v", got, want)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("socket phải quyền 600, đang %o", st.Mode().Perm())
		}
	}

	// Đang có server dùng socket thì tiến trình thứ hai không được chiếm.
	// Bị khóa do nhập sai nhiều: login-code thấy được, -unlock mở được.
	for i := 0; i < globalMaxFails; i++ {
		m.lim.begin("10.0.0." + string(rune('a'+i)))
	}
	if got, _ = FetchCodes(path); got.LockedUntil.IsZero() {
		t.Fatal("login-code phải thấy đang bị khóa")
	}
	if err := UnlockLogin(path); err != nil {
		t.Fatal(err)
	}
	if got, _ = FetchCodes(path); !got.LockedUntil.IsZero() {
		t.Fatal("mở khóa xong vẫn còn khóa")
	}
	if a := m.lim.begin("10.0.0.a"); !a.ok {
		t.Fatal("mở khóa xong phải đăng nhập được")
	}

	if err := ServeControl(context.Background(), path, m, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("socket đang dùng thì phải báo lỗi")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("tắt server thì phải xóa file socket")
	}
}

// Thư mục chứa socket quyền rộng hơn 700, hoặc đường dẫn trỏ vào file thường: từ chối
// và không xóa gì. Windows không có quyền kiểu Unix nên bỏ qua.
func TestControlSocketRefusesUnsafePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("quyền thư mục kiểu Unix")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := NewManager("", log)

	dir, err := os.MkdirTemp("", "scsc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	open := filepath.Join(dir, "open")
	os.Mkdir(open, 0o700)
	os.Chmod(open, 0o755) // Chmod để không phụ thuộc umask
	if err := ServeControl(context.Background(), filepath.Join(open, "c.sock"), m, log); err == nil ||
		!strings.Contains(err.Error(), "700") {
		t.Fatalf("thư mục 755 phải bị từ chối: %v", err)
	}

	file := filepath.Join(dir, "not-a-socket")
	os.WriteFile(file, []byte("du lieu quan trong"), 0o600)
	if err := ServeControl(context.Background(), file, m, log); err == nil {
		t.Fatal("đường dẫn là file thường phải bị từ chối")
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "du lieu quan trong" {
		t.Fatal("không được xóa file thường")
	}

	// Thư mục chưa có thì được tạo quyền 700.
	fresh := filepath.Join(dir, "run")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeControl(ctx, filepath.Join(fresh, "c.sock"), m, log) }()
	for i := 0; i < 100; i++ {
		if _, err = FetchCodes(filepath.Join(fresh, "c.sock")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(fresh); st.Mode().Perm() != 0o700 {
		t.Fatalf("thư mục tự tạo phải 700, đang %o", st.Mode().Perm())
	}
	cancel()
	<-done
}

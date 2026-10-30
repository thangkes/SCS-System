package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"
)

// ServeControl mở Unix socket để lệnh `scs-server login-code` lấy mã đăng nhập
// đang nằm trong RAM của server. Socket quyền 600 trong thư mục quyền 700: chỉ
// user chạy dịch vụ (hoặc root) đọc được, tức là phải SSH được vào server.
//
// Thư mục chứa socket bắt buộc quyền 700 (không chỉ socket 600): socket được tạo
// theo umask rồi mới chmod, thư mục kín thì khoảnh khắc đó không ai khác chen vào được.
func ServeControl(ctx context.Context, path string, m *Manager, log *slog.Logger) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("control socket: tạo thư mục: %w", err)
	}
	if err := checkPrivateDir(dir); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil {
		// Chỉ đụng tới file socket: đường dẫn cấu hình nhầm vào file thường thì báo lỗi, không xóa.
		if runtime.GOOS != "windows" && st.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("control socket: %s đã tồn tại và không phải socket; kiểm tra SCS_CONTROL_SOCKET", path)
		}
		// Còn file socket: nếu kết nối được thì có server khác đang chạy.
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return fmt.Errorf("control socket %s đang được tiến trình khác dùng", path)
		}
		os.Remove(path) // socket cũ do lần chạy trước tắt đột ngột
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("control socket: đặt quyền 600: %w", err)
	}

	var lastLog atomic.Int64 // `login-code -watch` hỏi mỗi giây: chỉ ghi log tối đa mỗi phút một lần
	mux := http.NewServeMux()
	mux.HandleFunc("GET /codes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(m.Snapshot())
		if now, prev := time.Now().Unix(), lastLog.Load(); now-prev > 60 && lastLog.CompareAndSwap(prev, now) {
			log.Info("đã xem mã đăng nhập qua lệnh login-code")
		}
	})
	mux.HandleFunc("POST /unlock", func(w http.ResponseWriter, r *http.Request) {
		m.Unlock()
		log.Warn("đã mở khóa đăng nhập web quản trị qua lệnh login-code -unlock")
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("control socket sẵn sàng (lệnh login-code)", "path", path)

	select {
	case err := <-errc:
		os.Remove(path)
		return fmt.Errorf("control socket: %w", err)
	case <-ctx.Done():
	}
	srv.Close()
	os.Remove(path)
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func controlClient(path string) *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}},
	}
}

// UnlockLogin mở khóa đăng nhập của server đang chạy qua control socket.
func UnlockLogin(path string) error {
	resp, err := controlClient(path).Post("http://scs-server/unlock", "", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("server trả %d", resp.StatusCode)
	}
	return nil
}

// checkPrivateDir đòi thư mục chứa socket chỉ chủ sở hữu vào được (700). Không tự
// chmod: đường dẫn có thể trỏ nhầm vào thư mục dùng chung (".", /tmp).
// Windows không có quyền kiểu Unix (chỉ dùng khi dev) nên bỏ qua.
func checkPrivateDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	st, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("control socket: thư mục %s đang quyền %o, phải là 700 vì ai vào được socket là lấy được mã "+
			"đăng nhập; chạy: chmod 700 %s (hoặc đặt SCS_CONTROL_SOCKET vào thư mục riêng)", dir, perm, dir)
	}
	return nil
}

// FetchCodes đọc mã hiện hành từ server đang chạy qua control socket.
func FetchCodes(path string) (Current, error) {
	resp, err := controlClient(path).Get("http://scs-server/codes")
	if err != nil {
		return Current{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Current{}, fmt.Errorf("server trả %d", resp.StatusCode)
	}
	var cur Current
	if err := json.NewDecoder(resp.Body).Decode(&cur); err != nil {
		return Current{}, err
	}
	return cur, nil
}

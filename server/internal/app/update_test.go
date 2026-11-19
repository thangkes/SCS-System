package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeUpdateFile(t *testing.T, running string, checked time.Time, commits string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "update-check.txt")
	s := "SCS-UPDATE-CHECK 1\nchecked=" + checked.Format(time.RFC3339) + "\nrunning=" + running +
		"\ntarget=origin/main\ntarget_desc=e3b09bd\nbehind=2\nmigrations=1\nerror=\n--\n" + commits
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Đọc kết quả check-update.sh: danh sách thay đổi (bỏ dòng Co-Authored-By), cờ đổi DB; file cũ / tính cho
// bản khác / không có file đều được báo đúng.
func TestReadUpdateInfo(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	commits := "\x1ee3b09bd\x1f2026-11-19T08:07:32+07:00\x1fTài liệu: số đo chạy thật\x1fCập nhật README.\n\nCo-Authored-By: Ai đó <a@example.com>\n" +
		"\x1ea982814\x1f2026-11-18T23:48:19+07:00\x1fĐăng nhập: khóa theo IP <script>\x1f- Sai mã 5 lần\n- Sai tên 10 lần\n"
	p := writeUpdateFile(t, "b6c7571", now.Add(-time.Hour), commits)
	info, err := readUpdateInfo(p, "b6c7571", now)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Configured || !info.Available || info.Stale || info.Outdated || info.Behind != 2 || !info.Migrations || len(info.Commits) != 2 {
		t.Fatalf("kết quả: %+v", info)
	}
	if c := info.Commits[0]; c.Hash != "e3b09bd" || c.Subject != "Tài liệu: số đo chạy thật" || len(c.Body) != 1 || c.Body[0] != "Cập nhật README." {
		t.Fatalf("commit 1 (phải bỏ Co-Authored-By): %+v", c)
	}
	if c := info.Commits[1]; len(c.Body) != 2 || !strings.Contains(c.Subject, "<script>") || c.Date.IsZero() {
		t.Fatalf("commit 2 (giữ nguyên chữ, web tự escape): %+v", c)
	}
	// Lần kiểm tra quá 2 ngày: cũ.
	if info, _ := readUpdateInfo(writeUpdateFile(t, "b6c7571", now.Add(-3*24*time.Hour), commits), "b6c7571", now); !info.Stale {
		t.Fatal("quá 2 ngày phải báo cũ")
	}
	// Server đã lên bản khác: danh sách không còn đúng.
	if info, _ := readUpdateInfo(p, "e3b09bd", now); !info.Outdated || info.Available || len(info.Commits) != 0 {
		t.Fatalf("bản đang chạy khác bản trong file: %+v", info)
	}
	// Không có file: chưa cài timer.
	if info, err := readUpdateInfo(filepath.Join(t.TempDir(), "khong-co"), "x", now); err != nil || info.Configured || info.Available {
		t.Fatalf("không có file: %+v %v", info, err)
	}
	// Sai định dạng.
	bad := filepath.Join(t.TempDir(), "bad")
	os.WriteFile(bad, []byte("rác"), 0o600)
	if _, err := readUpdateInfo(bad, "x", now); err == nil {
		t.Fatal("sai định dạng phải báo lỗi")
	}
}

package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bootEnv dựng thư mục cài (bản khung v1) + thư mục trạng thái có bản v2 đã tải và chạy tốt.
func bootEnv(t *testing.T) (state, install, pub string, clock *time.Time) {
	t.Helper()
	state, pub, priv := setup(t)
	install = filepath.Join(t.TempDir(), "opt")
	os.MkdirAll(install, 0o755)
	os.WriteFile(filepath.Join(install, binName), []byte("agent v1"), 0o755)
	data := []byte("agent v2")
	if err := Apply(context.Background(), &fakeDL{data: data}, release(priv, "v2", data), state, pub); err != nil {
		t.Fatal(err)
	}
	c := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	clock = &c
	now = func() time.Time { return *clock }
	// "version" của một binary giả = nội dung sau chữ "agent ".
	probeVersion = func(p string) (string, error) {
		b, err := os.ReadFile(p)
		return strings.TrimPrefix(string(b), "agent "), err
	}
	ownedByRoot = func(string) error { return nil }
	t.Cleanup(func() { now, probeVersion, ownedByRoot = time.Now, probeVersionOS, ownedByRootOS })
	t.Setenv(ChildEnv, "")
	return state, install, pub, clock
}

func installed(t *testing.T, install string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(install, binName))
	return string(b)
}

func TestPromoteFlow(t *testing.T) {
	state, install, pub, clock := bootEnv(t)

	// Bản mới chưa chạy tốt → không thay.
	if msg, err := Promote(state, install, pub); err != nil || msg != "" {
		t.Fatalf("%q %v", msg, err)
	}
	// Chạy tốt nhưng chưa đủ lâu → chưa thay.
	t.Setenv(ChildEnv, "1")
	if err := MarkGood(state, "v2"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ChildEnv, "")
	*clock = clock.Add(PromoteAfter - time.Minute)
	if msg, _ := Promote(state, install, pub); msg != "" || installed(t, install) != "agent v1" {
		t.Fatalf("chưa đủ %s mà đã thay: %q", PromoteAfter, msg)
	}
	// Đủ lâu → thay bản khung, giữ bản cũ.
	*clock = clock.Add(2 * time.Minute)
	msg, err := Promote(state, install, pub)
	if err != nil || !strings.Contains(msg, "đã thay bản khung v1 bằng v2") {
		t.Fatalf("%q %v", msg, err)
	}
	if installed(t, install) != "agent v2" {
		t.Fatal("bản khung phải là v2")
	}
	if prev, _ := os.ReadFile(filepath.Join(install, binName+".prev")); string(prev) != "agent v1" {
		t.Fatal("phải giữ bản khung cũ")
	}
	// Chưa có dấu khởi động: chờ, không làm gì thêm.
	*clock = clock.Add(5 * time.Hour)
	if msg, err := Promote(state, install, pub); err != nil || msg != "" {
		t.Fatalf("%q %v", msg, err)
	}
	// Bản khung mới khởi động và gọi được server → xác nhận, bỏ bản cũ.
	StampStart(state, "v2")
	StampOK(state, "v2")
	if msg, _ := Promote(state, install, pub); !strings.Contains(msg, "đã chạy tốt") {
		t.Fatalf("%q", msg)
	}
	if _, err := os.Stat(filepath.Join(install, binName+".prev")); err == nil {
		t.Fatal("bản cũ phải được dọn sau khi bản mới chạy tốt")
	}
	// Lượt sau: bản khung đã là v2, không còn gì để làm.
	if msg, err := Promote(state, install, pub); err != nil || msg != "" {
		t.Fatalf("%q %v", msg, err)
	}
}

func TestPromoteRollback(t *testing.T) {
	state, install, pub, clock := bootEnv(t)
	t.Setenv(ChildEnv, "1")
	MarkGood(state, "v2")
	t.Setenv(ChildEnv, "")
	*clock = clock.Add(PromoteAfter)
	if msg, err := Promote(state, install, pub); err != nil || msg == "" {
		t.Fatalf("%q %v", msg, err)
	}
	// Bản khung mới khởi động nhưng không gọi được server.
	*clock = clock.Add(time.Minute)
	StampStart(state, "v2")
	*clock = clock.Add(RollbackAfter - time.Minute)
	if msg, _ := Promote(state, install, pub); msg != "" || installed(t, install) != "agent v2" {
		t.Fatalf("chưa đủ %s mà đã quay về: %q", RollbackAfter, msg)
	}
	*clock = clock.Add(2 * time.Minute)
	msg, err := Promote(state, install, pub)
	if err != nil || !strings.Contains(msg, "đã quay về bản khung cũ") {
		t.Fatalf("%q %v", msg, err)
	}
	if installed(t, install) != "agent v1" {
		t.Fatal("phải quay về v1")
	}
	// Không thay lại bằng bản đã phải quay về.
	*clock = clock.Add(48 * time.Hour)
	if msg, _ := Promote(state, install, pub); msg != "" || installed(t, install) != "agent v1" {
		t.Fatalf("bản đã quay về không được thay lại: %q", msg)
	}
	// Dấu khởi động của bản con (ChildEnv) không được ghi.
	os.Remove(filepath.Join(state, startStamp))
	t.Setenv(ChildEnv, "1")
	StampStart(state, "v9")
	StampOK(state, "v9")
	if _, err := os.Stat(filepath.Join(state, startStamp)); err == nil {
		t.Fatal("bản con không được ghi dấu của bản khung")
	}
}

// Thư mục trạng thái do agent ghi: mọi thứ trong đó bị sửa thì tác vụ root phải từ chối.
func TestPromoteRejectsTampering(t *testing.T) {
	for name, tamper := range map[string]func(state string){
		"file bản mới bị sửa": func(state string) {
			os.WriteFile(binPath(state, "v2"), []byte("agent v2 độc"), 0o700)
		},
		"đổi phiên bản trong current.json": func(state string) {
			cur, _ := Load(state)
			os.Rename(binPath(state, "v2"), binPath(state, "v3"))
			cur.Version = "v3"
			save(state, cur)
		},
		"đổi SHA-256 cho khớp file tự chế": func(state string) {
			cur, _ := Load(state)
			os.WriteFile(binPath(state, "v2"), []byte("agent v2"), 0o700)
			cur.SHA256 = strings.Repeat("ab", 32)
			save(state, cur)
		},
		"file bản mới là thư mục": func(state string) {
			os.Remove(binPath(state, "v2"))
			os.Mkdir(binPath(state, "v2"), 0o700)
		},
		"phiên bản chứa đường dẫn": func(state string) {
			cur, _ := Load(state)
			cur.Version = "../../etc/x"
			save(state, cur)
		},
	} {
		state, install, pub, clock := bootEnv(t)
		t.Setenv(ChildEnv, "1")
		MarkGood(state, "v2")
		t.Setenv(ChildEnv, "")
		*clock = clock.Add(PromoteAfter)
		tamper(state)
		msg, err := Promote(state, install, pub)
		if err == nil || msg != "" {
			t.Errorf("%s: phải từ chối, được %q", name, msg)
		}
		if installed(t, install) != "agent v1" {
			t.Errorf("%s: bản khung bị thay", name)
		}
		if _, err := os.Stat(filepath.Join(install, binName+".new")); err == nil {
			t.Errorf("%s: còn sót file tạm", name)
		}
	}
	// Bản qua được chữ ký nhưng không chạy được trên máy (version trả sai) → không thay.
	state, install, pub, clock := bootEnv(t)
	t.Setenv(ChildEnv, "1")
	MarkGood(state, "v2")
	t.Setenv(ChildEnv, "")
	*clock = clock.Add(PromoteAfter)
	probeVersion = func(p string) (string, error) {
		if strings.HasSuffix(p, ".new") {
			return "hỏng", nil
		}
		return "v1", nil
	}
	if msg, err := Promote(state, install, pub); err == nil || installed(t, install) != "agent v1" {
		t.Fatalf("bản không chạy được không được làm bản khung: %q", msg)
	}
	// Thư mục cài hoặc khóa phát hành không thuộc root → không làm gì.
	ownedByRoot = func(p string) error {
		if p == pub {
			return os.ErrPermission
		}
		return nil
	}
	if _, err := Promote(state, install, pub); err == nil {
		t.Fatal("khóa phát hành không thuộc root phải bị từ chối")
	}
	// bootstrap.json hỏng → dừng, không đoán.
	ownedByRoot = func(string) error { return nil }
	os.WriteFile(filepath.Join(install, bootStateFile), []byte("{"), 0o644)
	if _, err := Promote(state, install, pub); err == nil {
		t.Fatal("bootstrap.json hỏng phải báo lỗi")
	}
	var bs BootState
	_ = json.Unmarshal([]byte(`{}`), &bs)
}

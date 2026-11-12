package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

type fakeDL struct {
	data  []byte
	calls int
}

func (f *fakeDL) Download(context.Context, string, int64) ([]byte, error) {
	f.calls++
	return f.data, nil
}

func setup(t *testing.T) (stateDir, pubPath string, priv ed25519.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	pubPath = filepath.Join(dir, "release.pub")
	os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644)
	return filepath.Join(dir, "state"), pubPath, priv
}

func release(priv ed25519.PrivateKey, version string, data []byte) protocol.UpdateInfo {
	sum := sha256.Sum256(data)
	u := protocol.UpdateInfo{Version: version, Arch: runtime.GOARCH, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	u.Sig = protocol.SignRelease(priv, u.Version, u.Arch, u.SHA256)
	return u
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// Tải bản mới → khung chuyển sang (tối đa MaxTries lần nếu chưa chạy tốt) → bản lỗi bị đánh dấu,
// khung chạy tiếp, không tải lại bản đó.
func TestApplyLaunchRollback(t *testing.T) {
	state, pub, priv := setup(t)
	data := []byte("agent v2")
	u := release(priv, "v2", data)
	if err := Apply(context.Background(), &fakeDL{data: data}, u, state, pub); err != nil {
		t.Fatal(err)
	}
	var launched []string
	execBinary = func(path string) error { launched = append(launched, path); return errors.New("thử") }
	defer func() { execBinary = execBinaryOS }()
	t.Setenv(ChildEnv, "")

	for i := 0; i < MaxTries; i++ {
		Launch(quiet, state, pub, "v1")
	}
	if len(launched) != MaxTries {
		t.Fatalf("phải thử chạy bản mới %d lần, được %d", MaxTries, len(launched))
	}
	Launch(quiet, state, pub, "v1") // lần thứ MaxTries+1: bản hỏng
	cur, _ := Load(state)
	if len(launched) != MaxTries || cur.Version != "" || len(cur.Bad) != 1 || cur.Bad[0] != "v2" {
		t.Fatalf("bản khởi động lỗi phải bị đánh dấu hỏng: %+v", cur)
	}
	if err := Apply(context.Background(), &fakeDL{data: data}, u, state, pub); !errors.Is(err, ErrBadRelease) {
		t.Fatalf("không được tải lại bản hỏng: %v", err)
	}
}

// Bản chạy tốt (đã gửi heartbeat) thì khởi động lại bao nhiêu lần vẫn chạy.
func TestMarkGood(t *testing.T) {
	state, pub, priv := setup(t)
	data := []byte("agent v3")
	Apply(context.Background(), &fakeDL{data: data}, release(priv, "v3", data), state, pub)
	n := 0
	execBinary = func(string) error { n++; return errors.New("thử") }
	defer func() { execBinary = execBinaryOS }()
	t.Setenv(ChildEnv, "")
	Launch(quiet, state, pub, "v1")
	if err := MarkGood(state, "v3"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		Launch(quiet, state, pub, "v1")
	}
	if n != 11 {
		t.Fatalf("bản chạy tốt phải luôn được chuyển sang, được %d lần", n)
	}
	// Đang là bản con (ChildEnv) hoặc đã đúng phiên bản thì không chuyển nữa.
	t.Setenv(ChildEnv, "1")
	Launch(quiet, state, pub, "v1")
	t.Setenv(ChildEnv, "")
	Launch(quiet, state, pub, "v3")
	if n != 11 {
		t.Fatal("không được chuyển tiếp lần nữa")
	}
}

// File bị sửa sau khi tải (hoặc chữ ký sai) thì không chạy.
func TestRejectTampered(t *testing.T) {
	state, pub, priv := setup(t)
	data := []byte("agent v4")
	u := release(priv, "v4", data)
	bad := u
	bad.SHA256 = hex.EncodeToString(make([]byte, 32))
	if Apply(context.Background(), &fakeDL{data: data}, bad, state, pub) == nil {
		t.Fatal("SHA-256 sai phải bị từ chối")
	}
	if err := Apply(context.Background(), &fakeDL{data: data}, u, state, pub); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(binPath(state, "v4"), []byte("agent v4 bị sửa"), 0o700)
	ran := false
	execBinary = func(string) error { ran = true; return nil }
	defer func() { execBinary = execBinaryOS }()
	t.Setenv(ChildEnv, "")
	Launch(quiet, state, pub, "v1")
	if ran {
		t.Fatal("file bị sửa không được chạy")
	}
	other := u
	other.Arch = "mips"
	if Apply(context.Background(), &fakeDL{data: data}, other, state, pub) == nil {
		t.Fatal("sai kiến trúc phải bị từ chối")
	}
}

// Máy khởi động lại (đổi boot_id) khi bản mới chưa gọi được server thì không tính là bản mới chết;
// chết trong cùng một lần bật máy thì có tính.
func TestRebootNotCounted(t *testing.T) {
	state, pub, priv := setup(t)
	data := []byte("agent v4")
	if err := Apply(context.Background(), &fakeDL{data: data}, release(priv, "v4", data), state, pub); err != nil {
		t.Fatal(err)
	}
	n := 0
	execBinary = func(string) error { n++; return errors.New("thử") }
	boot := "boot-1"
	bootID = func() string { return boot }
	defer func() { execBinary, bootID = execBinaryOS, readBootID }()
	t.Setenv(ChildEnv, "")

	// Cửa hàng tắt máy mỗi tối, mạng tới server đứt 10 ngày: bản mới vẫn được giữ.
	for i := 0; i < 10; i++ {
		boot = fmt.Sprintf("boot-%d", i)
		Launch(quiet, state, pub, "v1")
	}
	if cur, _ := Load(state); n != 10 || cur.Version != "v4" || cur.Tries != 0 || len(cur.Bad) != 0 {
		t.Fatalf("reboot không được tính là lỗi: n=%d %+v", n, cur)
	}
	// Bản mới chết liên tục trong cùng một lần bật máy: lần thứ MaxTries thì quay về bản khung.
	for i := 0; i < MaxTries; i++ {
		Launch(quiet, state, pub, "v1")
	}
	cur, _ := Load(state)
	if n != 10+MaxTries-1 || cur.Version != "" || len(cur.Bad) != 1 || cur.Bad[0] != "v4" {
		t.Fatalf("chết %d lần phải bị đánh dấu hỏng: n=%d %+v", MaxTries, n, cur)
	}
}

// Dấu hỏng tự hết sau BadFor: tải lại được (lỡ đánh dấu oan thì máy tự thử lại).
func TestBadExpires(t *testing.T) {
	state, pub, priv := setup(t)
	data := []byte("agent v5")
	u := release(priv, "v5", data)
	t0 := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	clock := t0
	now = func() time.Time { return clock }
	defer func() { now = time.Now }()
	if err := save(state, Current{Bad: []string{"v5"}, BadAt: map[string]time.Time{"v5": t0}}); err != nil {
		t.Fatal(err)
	}
	clock = t0.Add(BadFor - time.Minute)
	if err := Apply(context.Background(), &fakeDL{data: data}, u, state, pub); !errors.Is(err, ErrBadRelease) {
		t.Fatalf("chưa hết hạn: phải từ chối, được %v", err)
	}
	clock = t0.Add(BadFor)
	if err := Apply(context.Background(), &fakeDL{data: data}, u, state, pub); err != nil {
		t.Fatalf("hết hạn: phải tải lại được: %v", err)
	}
	if cur, _ := Load(state); cur.Version != "v5" || len(cur.Bad) != 0 || cur.BadAt != nil {
		t.Fatalf("%+v", cur)
	}
}

// Dấu hỏng ghi bởi bản agent cũ (không có thời điểm) vẫn chặn, rồi hết hạn tính từ lần đọc đầu.
func TestLegacyBad(t *testing.T) {
	state, pub, priv := setup(t)
	data := []byte("agent v6")
	u := release(priv, "v6", data)
	os.MkdirAll(dir(state), 0o700)
	os.WriteFile(filepath.Join(dir(state), "current.json"), []byte(`{"tries":0,"good":false,"bad":["v6"]}`), 0o600)
	if err := Apply(context.Background(), &fakeDL{data: data}, u, state, pub); !errors.Is(err, ErrBadRelease) {
		t.Fatalf("dấu hỏng cũ phải còn chặn: %v", err)
	}
}

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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

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

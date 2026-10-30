package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestReleaseSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	data := []byte("binary agent")
	sum := sha256.Sum256(data)
	u := UpdateInfo{Version: "v1.2.0-3-gabc", Arch: "amd64", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	u.Sig = SignRelease(priv, u.Version, u.Arch, u.SHA256)
	if err := VerifyRelease(pub, u, data); err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		name string
		u    UpdateInfo
		data []byte
	}{
		{"nội dung bị sửa", u, []byte("binary agenT")},
		{"đổi phiên bản", func() UpdateInfo { x := u; x.Version = "v9"; return x }(), data},
		{"đổi kiến trúc", func() UpdateInfo { x := u; x.Arch = "arm64"; return x }(), data},
		{"phiên bản có ..", func() UpdateInfo { x := u; x.Version = "a..b"; return x }(), data},
	}
	for _, c := range bad {
		if VerifyRelease(pub, c.u, c.data) == nil {
			t.Errorf("%s: phải bị từ chối", c.name)
		}
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if VerifyRelease(other, u, data) == nil {
		t.Error("khóa khác: phải bị từ chối")
	}
	for _, v := range []string{"", "../x", "a/b", "-x"} {
		if ValidVersion(v) {
			t.Errorf("%q không được là phiên bản hợp lệ", v)
		}
	}
}

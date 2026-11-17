package scan

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"testing"
)

func TestReport(t *testing.T) {
	state, pub, priv := setup(t)
	if r := Report(state, pub, nil); r.Installed || r.Name != "" || r.Error != "" {
		t.Fatalf("chưa cài: %+v", r)
	}
	data := bundle(t, priv, "1")
	if _, err := Install(state, pub, data); err != nil {
		t.Fatal(err)
	}
	r := Report(state, pub, nil)
	if !r.Installed || r.Name != "t" || r.Version != "1" || len(r.SHA256) != 64 || r.Total < 1 || r.Active != r.Total || r.Scanning {
		t.Fatalf("đã cài: %+v", r)
	}
	// Bộ đang cài bị sửa / sai chữ ký: báo lỗi, không coi là đã cài.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if err := os.WriteFile(BundlePath(state), bundle(t, other, "2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := Report(state, pub, nil); r.Installed || r.Error == "" || r.Version != "2" {
		t.Fatalf("sai chữ ký: %+v", r)
	}
}

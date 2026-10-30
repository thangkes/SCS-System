package pki

import (
	"path/filepath"
	"testing"
)

func TestReleaseKey(t *testing.T) {
	keyPEM, pubPEM, err := NewReleaseKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "release.key")
	if err := WriteFile(path, keyPEM, true); err != nil {
		t.Fatal(err)
	}
	priv, err := LoadReleaseKey(path)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParseReleasePub(pubPEM)
	if err != nil || !pub.Equal(priv.Public()) {
		t.Fatalf("khóa công khai phải khớp khóa riêng: %v", err)
	}
}

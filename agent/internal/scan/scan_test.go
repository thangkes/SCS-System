package scan

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/common/rules"
)

func setup(t *testing.T) (stateDir, pubPath string, priv ed25519.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	pubPath = filepath.Join(dir, "release.pub")
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "state"), pubPath, priv
}

func bundle(t *testing.T, priv ed25519.PrivateKey, version string) []byte {
	t.Helper()
	b, err := rules.Build(&rules.RuleSet{Name: "t", Version: version, Rules: []rules.Rule{
		{ID: "OOM-01", Severity: "high", Source: rules.SourceJournald, Unit: "kernel", Match: `Out of memory: Killed process \d+ \((?P<proc>[^)]+)\)`},
	}}, priv)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(b)
	return data
}

func TestInstallLoadRun(t *testing.T) {
	state, pub, priv := setup(t)
	if _, err := Load(state, pub); err != ErrNoBundle {
		t.Fatalf("chưa cài: %v", err)
	}
	if _, err := Install(state, pub, bundle(t, priv, "1")); err != nil {
		t.Fatal(err)
	}
	// Bộ rule ký bằng khóa khác: bị từ chối, bộ cũ vẫn giữ.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Install(state, pub, bundle(t, other, "2")); err == nil {
		t.Fatal("khóa khác: phải bị từ chối")
	}
	s, err := Load(state, pub)
	if err != nil || s.Version != "1" {
		t.Fatalf("phải giữ bộ cũ: %v", err)
	}
	e, err := rules.CompileFor(s, rules.Context{})
	if err != nil {
		t.Fatal(err)
	}
	log := "Out of memory: Killed process 42 (java) total-vm:1kB\nnothing here\n"
	var got []rules.Event
	err = Run(context.Background(), e, ReaderSource{R: strings.NewReader(log), Source: rules.SourceJournald, Unit: "kernel"},
		func(ev rules.Event) { got = append(got, ev) })
	if err != nil || len(got) != 1 || got[0].Fields["proc"] != "java" {
		t.Fatalf("err=%v events=%+v", err, got)
	}
}

// Bộ chỉ có sơ đồ LAN (không rule quét log nào) vẫn cài và nạp được (kiểm kê thiết bị LAN, schema 4).
func TestInstallLANOnly(t *testing.T) {
	state, pub, priv := setup(t)
	b, err := rules.Build(&rules.RuleSet{Schema: rules.LANSchema, Name: "lan", Version: "1",
		LAN: &rules.LAN{Targets: []rules.LANTarget{{IP: "192.168.1.10", Type: "can"}}}}, priv)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(b)
	if _, err := Install(state, pub, data); err != nil {
		t.Fatalf("cài bộ chỉ có sơ đồ LAN: %v", err)
	}
	s, err := Load(state, pub)
	if err != nil || s.LAN == nil || len(s.LAN.Targets) != 1 {
		t.Fatalf("nạp bộ chỉ có sơ đồ LAN: %v %+v", err, s)
	}
}

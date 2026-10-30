package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestCA(t *testing.T) (*CA, string, string) {
	t.Helper()
	ca, keyPEM, err := NewCA("scs-agent CA test")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cf, kf := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key")
	if err := WriteFile(cf, ca.CertPEM, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(kf, keyPEM, true); err != nil {
		t.Fatal(err)
	}
	return ca, cf, kf
}

func pool(ca *CA) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

func TestClientCertFromAgentKey(t *testing.T) {
	_, cf, kf := newTestCA(t)
	ca, err := Load(cf, kf)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss, err := ca.IssueClient(pub, "42")
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(iss.PEM)
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool(ca), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("chứng chỉ agent phải được CA kiểm: %v", err)
	}
	if !pub.Equal(cert.PublicKey) || cert.Subject.CommonName != "42" || iss.Serial != SerialHex(cert.SerialNumber) {
		t.Fatal("chứng chỉ phải mang đúng khóa của máy, mã máy và serial")
	}
	// Agent dùng khóa riêng Ed25519 của nó với chứng chỉ này trong TLS.
	if _, err := tls.X509KeyPair(iss.PEM, mustPKCS8(t, priv)); err != nil {
		t.Fatalf("cặp chứng chỉ + khóa agent phải dùng được cho TLS: %v", err)
	}
	if _, err := ca.IssueClient([]byte("ngan"), "1"); err == nil {
		t.Fatal("khóa sai độ dài phải lỗi")
	}
}

func TestServerCertWithWANIP(t *testing.T) {
	ca, _, _ := newTestCA(t)
	certPEM, keyPEM, err := ca.IssueServer([]net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("10.8.0.1")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(tc.Certificate[0])
	for _, ip := range []string{"203.0.113.10", "10.8.0.1"} {
		if err := leaf.VerifyHostname(ip); err != nil {
			t.Errorf("chứng chỉ server phải hợp lệ cho %s: %v", ip, err)
		}
	}
	if err := leaf.VerifyHostname("198.51.100.1"); err == nil {
		t.Error("IP không ghi trong chứng chỉ phải bị từ chối")
	}
	if _, _, err := ca.IssueServer(nil, nil); err == nil {
		t.Error("không có IP/tên phải lỗi")
	}
}

func TestLoadRejects(t *testing.T) {
	_, cf, kf := newTestCA(t)
	if runtime.GOOS != "windows" {
		os.Chmod(kf, 0o644)
		if _, err := Load(cf, kf); err == nil || !strings.Contains(err.Error(), "600") {
			t.Fatalf("khóa CA quyền 644 phải bị từ chối: %v", err)
		}
		os.Chmod(kf, 0o600)
	}
	// Khóa của CA khác không khớp chứng chỉ.
	_, _, kf2 := newTestCA(t)
	if _, err := Load(cf, kf2); err == nil {
		t.Fatal("khóa không khớp chứng chỉ CA phải lỗi")
	}
	if err := WriteFile(cf, []byte("x"), false); err == nil {
		t.Fatal("không được ghi đè file CA đã có")
	}
}

func mustPKCS8(t *testing.T, k ed25519.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

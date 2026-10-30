package httpx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeServerCert ghi cặp chứng chỉ/khóa tự ký với serial và hạn cho trước, đổi mtime để chắc
// chắn khác lần ghi trước (hệ thống file có thể làm tròn thời gian).
func writeServerCert(t *testing.T, certFile, keyFile string, serial int64, notAfter time.Time, bump time.Duration) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "scs-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kder, _ := x509.MarshalECPrivateKey(key)
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600)
	mt := time.Now().Add(bump)
	os.Chtimes(certFile, mt, mt)
	os.Chtimes(keyFile, mt, mt)
}

func servedSerial(t *testing.T, addr string) int64 {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
}

// step-ca gia hạn chứng chỉ (ghi đè file) → kết nối mới nhận chứng chỉ mới mà không cần
// khởi động lại server. Trước khi sửa: server giữ chứng chỉ cũ tới khi restart.
func TestCertReloadOnRenew(t *testing.T) {
	old := CertReloadEvery
	CertReloadEvery = 0
	t.Cleanup(func() { CertReloadEvery = old })

	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "srv.pem"), filepath.Join(dir, "srv.key")
	writeServerCert(t, certFile, keyFile, 100, time.Now().Add(30*24*time.Hour), 0)

	logs := &lockedBuffer{}
	cfg, err := ServerTLS(certFile, keyFile, "", NoClientCert, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Listener = tls.NewListener(srv.Listener, cfg)
	srv.Start()
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	if s := servedSerial(t, addr); s != 100 {
		t.Fatalf("chứng chỉ ban đầu serial %d, muốn 100", s)
	}

	// Gia hạn.
	writeServerCert(t, certFile, keyFile, 200, time.Now().Add(30*24*time.Hour), 2*time.Second)
	if s := servedSerial(t, addr); s != 200 {
		t.Fatalf("sau gia hạn server vẫn đưa serial %d, muốn 200", s)
	}
	if !strings.Contains(logs.String(), "đã nạp chứng chỉ TLS mới") {
		t.Error("phải ghi log khi nạp chứng chỉ mới")
	}

	// File mới hỏng (đang ghi dở): giữ chứng chỉ đang chạy, ghi log lỗi một lần.
	os.WriteFile(certFile, []byte("-----BEGIN CERTIFICATE-----\nhong\n"), 0o600)
	mt := time.Now().Add(4 * time.Second)
	os.Chtimes(certFile, mt, mt)
	for i := 0; i < 3; i++ {
		if s := servedSerial(t, addr); s != 200 {
			t.Fatalf("file hỏng thì phải giữ chứng chỉ cũ, server đưa serial %d", s)
		}
	}
	if n := strings.Count(logs.String(), "chứng chỉ TLS mới bị lỗi"); n != 1 {
		t.Errorf("lỗi file mới phải ghi log đúng 1 lần, được %d", n)
	}

	// Ghi lại đúng → nạp được.
	writeServerCert(t, certFile, keyFile, 300, time.Now().Add(30*24*time.Hour), 6*time.Second)
	if s := servedSerial(t, addr); s != 300 {
		t.Fatalf("ghi lại file đúng thì phải nạp, server đưa serial %d", s)
	}
}

// Chứng chỉ còn dưới 7 ngày (gia hạn hỏng) thì cảnh báo trong log, tối đa mỗi giờ một lần.
func TestCertExpiryWarning(t *testing.T) {
	old := CertReloadEvery
	CertReloadEvery = 0
	t.Cleanup(func() { CertReloadEvery = old })
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "srv.pem"), filepath.Join(dir, "srv.key")
	writeServerCert(t, certFile, keyFile, 1, time.Now().Add(20*time.Hour), 0)
	logs := &lockedBuffer{}
	cfg, err := ServerTLS(certFile, keyFile, "", NoClientCert, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		cfg.GetCertificate(&tls.ClientHelloInfo{})
	}
	if n := strings.Count(logs.String(), "sắp hết hạn"); n != 1 {
		t.Fatalf("cảnh báo hết hạn phải đúng 1 lần, được %d", n)
	}
}

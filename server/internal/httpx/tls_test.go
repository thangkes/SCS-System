package httpx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testPKI struct {
	dir                       string
	caFile, certFile, keyFile string
	caPool                    *x509.CertPool
	client                    tls.Certificate // chứng chỉ client do CA ký
	stranger                  tls.Certificate // chứng chỉ client tự ký, không thuộc CA
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	p := &testPKI{dir: t.TempDir()}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "scs-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	p.caPool = x509.NewCertPool()
	p.caPool.AddCert(caCert)
	p.caFile = p.writePEM("ca.pem", "CERTIFICATE", caDER)

	issue := func(serial int64, cn string, usage x509.ExtKeyUsage, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) ([]byte, *ecdsa.PrivateKey) {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		if parent == nil {
			parent, parentKey = tmpl, key
		}
		der, _ := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
		return der, key
	}

	srvDER, srvKey := issue(2, "localhost", x509.ExtKeyUsageServerAuth, caCert, caKey)
	p.certFile = p.writePEM("srv.pem", "CERTIFICATE", srvDER)
	keyDER, _ := x509.MarshalECPrivateKey(srvKey)
	p.keyFile = p.writePEM("srv.key", "EC PRIVATE KEY", keyDER)

	cliDER, cliKey := issue(3, "DEV-1234-01", x509.ExtKeyUsageClientAuth, caCert, caKey)
	p.client = tls.Certificate{Certificate: [][]byte{cliDER}, PrivateKey: cliKey}
	strDER, strKey := issue(4, "DEV-1234-01", x509.ExtKeyUsageClientAuth, nil, nil)
	p.stranger = tls.Certificate{Certificate: [][]byte{strDER}, PrivateKey: strKey}
	return p
}

func (p *testPKI) writePEM(name, typ string, der []byte) string {
	path := filepath.Join(p.dir, name)
	os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600)
	return path
}

// startTLS chạy server thật với cấu hình từ ServerTLS; handler trả 200 nếu có chứng chỉ client đã kiểm.
func startTLS(t *testing.T, p *testPKI, mode ClientAuth) string {
	t.Helper()
	cfg, err := ServerTLS(p.certFile, p.keyFile, p.caFile, mode, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.VerifiedChains) > 0 {
			w.Write([]byte("verified"))
			return
		}
		w.Write([]byte("anonymous"))
	}))
	// Tự bọc listener TLS (không dùng StartTLS: httptest sẽ chèn chứng chỉ riêng vào Certificates).
	srv.Listener = tls.NewListener(srv.Listener, cfg)
	srv.Start()
	t.Cleanup(srv.Close)
	return "https://" + srv.Listener.Addr().String()
}

func get(t *testing.T, url string, pool *x509.CertPool, certs []tls.Certificate, maxVer uint16) (string, error) {
	t.Helper()
	tc := &tls.Config{RootCAs: pool, MaxVersion: maxVer}
	if len(certs) > 0 {
		// Luôn gửi chứng chỉ, kể cả khi không khớp CA server công bố
		// (client Go mặc định sẽ lặng lẽ không gửi) — mô phỏng agent giả mạo.
		tc.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &certs[0], nil
		}
	}
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tc}}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b := make([]byte, 32)
	n, _ := resp.Body.Read(b)
	return string(b[:n]), nil
}

func TestRequireClientCert(t *testing.T) {
	p := newTestPKI(t)
	url := startTLS(t, p, RequireClientCert)

	if _, err := get(t, url, p.caPool, nil, 0); err == nil {
		t.Error("không chứng chỉ client phải bị từ chối khi bắt tay TLS")
	}
	if _, err := get(t, url, p.caPool, []tls.Certificate{p.stranger}, 0); err == nil {
		t.Error("chứng chỉ không do CA ký phải bị từ chối")
	}
	if body, err := get(t, url, p.caPool, []tls.Certificate{p.client}, 0); err != nil || body != "verified" {
		t.Errorf("chứng chỉ hợp lệ: body=%q err=%v", body, err)
	}
}

func TestOptionalClientCert(t *testing.T) {
	p := newTestPKI(t)
	url := startTLS(t, p, OptionalClientCert)

	if body, err := get(t, url, p.caPool, nil, 0); err != nil || body != "anonymous" {
		t.Errorf("máy mới chưa có chứng chỉ phải vào được: body=%q err=%v", body, err)
	}
	if body, err := get(t, url, p.caPool, []tls.Certificate{p.client}, 0); err != nil || body != "verified" {
		t.Errorf("có chứng chỉ: body=%q err=%v", body, err)
	}
	if _, err := get(t, url, p.caPool, []tls.Certificate{p.stranger}, 0); err == nil {
		t.Error("đưa chứng chỉ lạ thì vẫn phải bị từ chối")
	}
}

func TestTLS12Rejected(t *testing.T) {
	p := newTestPKI(t)
	url := startTLS(t, p, NoClientCert)
	if _, err := get(t, url, p.caPool, nil, tls.VersionTLS12); err == nil {
		t.Error("client chỉ hỗ trợ TLS 1.2 phải bị từ chối")
	}
}

func TestServerTLSNoCert(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := ServerTLS("", "", "", RequireClientCert, discard)
	if cfg != nil || err != nil {
		t.Fatalf("không khai chứng chỉ phải trả nil, nil; được %v, %v", cfg, err)
	}
	if _, err := ServerTLS("khong-co.pem", "khong-co.key", "", NoClientCert, discard); err == nil {
		t.Fatal("file chứng chỉ không tồn tại phải lỗi")
	}
}

func TestSelfSignedTLS(t *testing.T) {
	cfg, fp, err := SelfSignedTLS()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	srv.TLS = cfg
	srv.StartTLS()
	defer srv.Close()

	// Không tin chứng chỉ tự ký → client từ chối (đúng như trình duyệt cảnh báo).
	if _, err := get(t, srv.URL, x509.NewCertPool(), nil, 0); err == nil {
		t.Fatal("chứng chỉ tự ký không được tin mặc định")
	}
	// Tin đúng chứng chỉ đó → kết nối được, dấu vân tay khớp với chứng chỉ server đưa ra.
	leaf, _ := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	if body, err := get(t, srv.URL, pool, nil, 0); err != nil || body != "ok" {
		t.Fatalf("tin chứng chỉ rồi vẫn lỗi: body=%q err=%v", body, err)
	}
	if fp != Fingerprint(leaf.Raw) || len(fp) != 95 {
		t.Fatalf("dấu vân tay sai: %s", fp)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatal("phải là TLS 1.3")
	}
}

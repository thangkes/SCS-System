package enroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/client"
	"github.com/thangkes/SCS-System/agent/internal/identity"
	"github.com/thangkes/SCS-System/common/protocol"
)

// fakeServer đóng vai scs-server: kiểm gói enroll thật (chữ ký, giờ, fingerprint), trả
// "chờ duyệt" pendingTimes lần rồi "đã duyệt" kèm chứng chỉ cấp từ khóa của agent.
type fakeServer struct {
	t            *testing.T
	caCert       *x509.Certificate
	caKey        *ecdsa.PrivateKey
	caFile       string
	url          string
	pendingTimes int
	reject       bool
	clockAhead   time.Duration // giờ server đi trước giờ máy (mô phỏng máy lệch giờ)

	mu      sync.Mutex
	calls   int
	skewErr int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t}
	f.caKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &f.caKey.PublicKey, f.caKey)
	f.caCert, _ = x509.ParseCertificate(der)
	f.caFile = filepath.Join(t.TempDir(), "server-ca.pem")
	os.WriteFile(f.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)

	// Chứng chỉ server ghi IP 127.0.0.1 (agent gọi thẳng bằng IP, như IP WAN thật).
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	stmpl := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	sder, _ := x509.CreateCertificate(rand.Reader, stmpl, f.caCert, &srvKey.PublicKey, f.caKey)
	pool := x509.NewCertPool()
	pool.AddCert(f.caCert)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/enroll", f.enroll)
	mux.HandleFunc("POST /v1/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, `{"error":"cần chứng chỉ client"}`, 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "cn": r.TLS.PeerCertificates[0].Subject.CommonName})
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = tls.NewListener(srv.Listener, &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{sder}, PrivateKey: srvKey}},
		ClientCAs:    pool, ClientAuth: tls.VerifyClientCertIfGiven,
	})
	srv.Start()
	t.Cleanup(srv.Close)
	f.url = "https://" + srv.Listener.Addr().String()
	return f
}

func (f *fakeServer) enroll(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().Add(f.clockAhead)
	w.Header().Set("Date", now.UTC().Format(http.TimeFormat))
	b, _ := io.ReadAll(r.Body)
	var s protocol.Signed
	json.Unmarshal(b, &s)
	env, err := s.ParseUnverified()
	if err != nil || env.Type != protocol.TypeEnroll {
		http.Error(w, `{"error":"sai định dạng"}`, 400)
		return
	}
	if d := now.Sub(env.TS); d > 5*time.Minute || d < -5*time.Minute {
		f.skewErr++
		http.Error(w, `{"error":"giờ lệch"}`, 400)
		return
	}
	var body protocol.EnrollBody
	env.DecodeBody(&body)
	pub, err := protocol.DecodePublicKey(body.PublicKey)
	if err != nil || s.Verify(pub) != nil || env.Fingerprint != body.Components.Hash() {
		http.Error(w, `{"error":"chữ ký sai"}`, 401)
		return
	}
	f.calls++
	switch {
	case f.reject:
		json.NewEncoder(w).Encode(protocol.EnrollResponse{Status: protocol.EnrollRejected})
	case f.calls <= f.pendingTimes:
		json.NewEncoder(w).Encode(protocol.EnrollResponse{Status: protocol.EnrollPending})
	default:
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(int64(100 + f.calls)), Subject: pkix.Name{CommonName: "7"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		der, _ := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, pub, f.caKey)
		json.NewEncoder(w).Encode(protocol.EnrollResponse{Status: protocol.EnrollApproved, DeviceID: "7",
			CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))})
	}
}

func setup(t *testing.T, f *fakeServer) (Deps, *client.Client) {
	t.Helper()
	old := []time.Duration{PendingEvery, BackoffMin, BackoffMax}
	PendingEvery, BackoffMin, BackoffMax = 10*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { PendingEvery, BackoffMin, BackoffMax = old[0], old[1], old[2] })

	dir := t.TempDir()
	key, err := identity.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.New(f.url, f.caFile, filepath.Join(dir, "key"), key, "0.1.0-test")
	if err != nil {
		t.Fatal(err)
	}
	return Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Client: c, Key: key,
		KeyDir: filepath.Join(dir, "key"), StateDir: filepath.Join(dir, "state"), CAFile: f.caFile}, c
}

// Chờ duyệt 2 lần → được duyệt → lưu chứng chỉ → gọi route bắt buộc mTLS bằng chứng chỉ đó.
func TestEnrollThenMutualTLS(t *testing.T) {
	f := newFakeServer(t)
	f.pendingTimes = 2
	d, c := setup(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := Run(ctx, d)
	if err != nil || st.Status != identity.StatusApproved || st.DeviceID != "7" {
		t.Fatalf("đăng ký: %+v %v", st, err)
	}
	if f.calls != 3 {
		t.Fatalf("server nhận %d gói enroll, muốn 3 (2 lần chờ + 1 lần duyệt)", f.calls)
	}
	if saved, _ := identity.LoadState(d.StateDir); saved.Status != identity.StatusApproved || saved.RequestedAt.IsZero() {
		t.Fatalf("trạng thái lưu: %+v", saved)
	}
	var out struct {
		OK bool   `json:"ok"`
		CN string `json:"cn"`
	}
	if err := c.Send(ctx, "/v1/heartbeat", protocol.TypeHeartbeat, "7", "", protocol.HeartbeatBody{}, &out); err != nil || !out.OK || out.CN != "7" {
		t.Fatalf("gọi route mTLS bằng chứng chỉ vừa nhận: %+v %v", out, err)
	}
}

// Đồng hồ máy chậm 10 phút: lần đầu server từ chối, agent học lệch giờ từ header Date rồi đăng ký được.
func TestEnrollLearnsClockSkew(t *testing.T) {
	f := newFakeServer(t)
	f.clockAhead = 10 * time.Minute
	d, c := setup(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := Run(ctx, d)
	if err != nil || st.Status != identity.StatusApproved {
		t.Fatalf("%+v %v", st, err)
	}
	if f.skewErr != 1 || c.Offset() < 9*time.Minute {
		t.Fatalf("phải bị từ chối đúng 1 lần rồi tự bù giờ; skewErr=%d offset=%v", f.skewErr, c.Offset())
	}
}

func TestEnrollRejected(t *testing.T) {
	f := newFakeServer(t)
	f.reject = true
	d, _ := setup(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := Run(ctx, d)
	if err != nil || st.Status != identity.StatusRejected {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := os.Stat(identity.CertPath(d.KeyDir)); err == nil {
		t.Fatal("bị từ chối thì không có chứng chỉ")
	}
}

// Server dùng chứng chỉ không do CA đóng trong gói ký (giả mạo server): agent không gửi gì được.
func TestRejectsUnknownServer(t *testing.T) {
	real := newFakeServer(t)
	other := newFakeServer(t) // CA khác
	d, _ := setup(t, real)
	c, _ := client.New(real.url, other.caFile, d.KeyDir, d.Key, "test")
	d.Client = c
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if st, _ := Run(ctx, d); st.Status == identity.StatusApproved || real.calls != 0 {
		t.Fatalf("không được nói chuyện với server lạ: %+v, server nhận %d gói", st, real.calls)
	}
}

// Chứng chỉ nhận về mang khóa của máy khác: không lưu.
func TestCheckCertRejectsForeignKey(t *testing.T) {
	f := newFakeServer(t)
	d, _ := setup(t, f)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "7"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, otherPub, f.caKey)
	resp := protocol.EnrollResponse{Status: protocol.EnrollApproved, DeviceID: "7",
		CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
	if err := checkCert(resp, d); err == nil {
		t.Fatal("chứng chỉ mang khóa máy khác phải bị từ chối")
	}
	resp.DeviceID = "8"
	if err := checkCert(resp, d); err == nil {
		t.Fatal("mã máy không khớp phải bị từ chối")
	}
}

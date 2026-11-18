package enroll

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/client"
	"github.com/thangkes/SCS-System/agent/internal/identity"
	"github.com/thangkes/SCS-System/common/protocol"
)

// Chứng chỉ máy đã hết hạn: agent không gửi nó lúc bắt tay TLS (nên vẫn kết nối được), heartbeat bị 401;
// Renew gửi lại gói enroll bằng chính khóa máy, nhận chứng chỉ mới 90 ngày, heartbeat chạy lại.
func TestRenewExpiredCert(t *testing.T) {
	f := newFakeServer(t)
	d, c := setup(t, f)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(50), Subject: pkix.Name{CommonName: "7"},
		NotBefore: time.Now().Add(-100 * 24 * time.Hour), NotAfter: time.Now().Add(-time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, d.Key.Public, f.caKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.SaveCert(d.KeyDir, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out struct {
		CN string `json:"cn"`
	}
	err = c.Send(ctx, "/v1/heartbeat", protocol.TypeHeartbeat, "7", "", protocol.HeartbeatBody{}, &out)
	var he *client.HTTPError
	if !errors.As(err, &he) || he.Status != 401 {
		t.Fatalf("chứng chỉ hết hạn: không gửi chứng chỉ, server trả 401; được %v", err)
	}

	// Server đang hết lượt: chưa cấp, không lỗi.
	f.pendingTimes = 1
	if ok, err := Renew(ctx, d, "7"); ok || err != nil {
		t.Fatalf("hết lượt: %v %v", ok, err)
	}
	// Server cấp cho mã máy khác: không lưu.
	if ok, err := Renew(ctx, d, "8"); ok || err == nil {
		t.Fatalf("khác mã máy phải lỗi: %v %v", ok, err)
	}
	if na, _ := identity.CertNotAfter(d.KeyDir); na.After(time.Now()) {
		t.Fatal("không được lưu chứng chỉ của mã máy khác")
	}
	if ok, err := Renew(ctx, d, "7"); !ok || err != nil {
		t.Fatalf("gia hạn: %v %v", ok, err)
	}
	if na, _ := identity.CertNotAfter(d.KeyDir); time.Until(na) < 80*24*time.Hour {
		t.Fatalf("chứng chỉ mới phải còn ~90 ngày: %s", na)
	}
	if err := c.Send(ctx, "/v1/heartbeat", protocol.TypeHeartbeat, "7", "", protocol.HeartbeatBody{}, &out); err != nil || out.CN != "7" {
		t.Fatalf("heartbeat bằng chứng chỉ mới: %v %+v", err, out)
	}
	f.reject = true
	if _, err := Renew(ctx, d, "7"); !errors.Is(err, ErrRejected) {
		t.Fatalf("máy bị thu hồi: %v", err)
	}
}

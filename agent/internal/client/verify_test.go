package client

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

func testCert(t *testing.T, cn string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, isCA bool, eku []x509.ExtKeyUsage, ips ...string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: isCA, BasicConstraintsValid: true, ExtKeyUsage: eku,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	for _, ip := range ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP(ip))
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

// Chứng chỉ server chỉ ghi IP LAN, agent gọi bằng IP khác (VLAN, IP WAN qua NAT) vẫn được;
// chứng chỉ của CA lạ hoặc chứng chỉ client (cùng CA) thì không.
func TestVerifyServerIgnoresAddress(t *testing.T) {
	ca, caKey := testCert(t, "CA", nil, nil, true, nil)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	verify := verifyServer(roots)

	srv, _ := testCert(t, "scs-server", ca, caKey, false, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, "192.168.1.10")
	if err := verify(tls.ConnectionState{ServerName: "203.0.113.5", PeerCertificates: []*x509.Certificate{srv}}); err != nil {
		t.Fatalf("chứng chỉ do CA cấp phải được nhận dù gọi bằng IP khác: %v", err)
	}

	client, _ := testCert(t, "42", ca, caKey, false, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	if verify(tls.ConnectionState{PeerCertificates: []*x509.Certificate{client}}) == nil {
		t.Fatal("chứng chỉ máy agent (chỉ dùng cho client) không được dùng giả làm server")
	}

	otherCA, otherKey := testCert(t, "CA lạ", nil, nil, true, nil)
	fake, _ := testCert(t, "fake", otherCA, otherKey, false, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, "192.168.1.10")
	if verify(tls.ConnectionState{PeerCertificates: []*x509.Certificate{fake}}) == nil {
		t.Fatal("chứng chỉ của CA lạ phải bị từ chối")
	}
	if verify(tls.ConnectionState{}) == nil {
		t.Fatal("không có chứng chỉ phải bị từ chối")
	}
}

// Package pki: CA nội bộ của scs-server. Cấp chứng chỉ server (ghi IP WAN để agent gọi
// thẳng bằng IP) và chứng chỉ client cho từng agent khi admin đã duyệt.
//
// Chứng chỉ agent được làm từ chính khóa Ed25519 máy gửi lúc đăng ký (không cần CSR):
// agent dùng một khóa cho cả mTLS lẫn ký gói tin. Khóa riêng không bao giờ rời máy.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Thời hạn chứng chỉ.
const (
	CAValidity     = 10 * 365 * 24 * time.Hour
	ServerValidity = 2 * 365 * 24 * time.Hour
	ClientValidity = 90 * 24 * time.Hour
	backdate       = 5 * time.Minute // chịu được đồng hồ máy lệch một chút
)

// CA giữ chứng chỉ và khóa ký của CA.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	key     crypto.Signer
}

// Load đọc CA từ file PEM. File khóa phải quyền 600.
func Load(certFile, keyFile string) (*CA, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("đọc chứng chỉ CA: %w", err)
	}
	if err := checkPrivate(keyFile); err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("đọc khóa CA: %w", err)
	}
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, errors.New("file CA không phải PEM hợp lệ")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("chứng chỉ CA: %w", err)
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("khóa CA: %w", err)
	}
	signer, ok := k.(crypto.Signer)
	if !ok || !cert.IsCA {
		return nil, errors.New("file CA không phải CA hợp lệ")
	}
	if !publicKeyEqual(cert.PublicKey, signer.Public()) {
		return nil, errors.New("khóa CA không khớp với chứng chỉ CA")
	}
	return &CA{Cert: cert, CertPEM: certPEM, key: signer}, nil
}

// NewCA sinh CA mới (ECDSA P-256).
func NewCA(name string) (*CA, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          randSerial(),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(CAValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return &CA{Cert: cert, CertPEM: certPEM(der), key: key}, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// IssueServer cấp chứng chỉ server ghi các IP / tên cho trước (agent gọi bằng IP WAN,
// admin vào web bằng IP VPN). Trả về chứng chỉ và khóa riêng dạng PEM.
func (ca *CA) IssueServer(ips []net.IP, dnsNames []string) (certPEMOut, keyPEM []byte, err error) {
	if len(ips) == 0 && len(dnsNames) == 0 {
		return nil, nil, errors.New("chứng chỉ server cần ít nhất một IP hoặc tên")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	cn := "scs-server"
	if len(ips) > 0 {
		cn = ips[0].String()
	}
	tmpl := &x509.Certificate{
		SerialNumber: randSerial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now.Add(-backdate),
		NotAfter:     now.Add(ServerValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  ips,
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return certPEM(der), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// Issued là chứng chỉ vừa cấp cho agent.
type Issued struct {
	PEM      []byte
	Serial   string // hex, lưu vào devices.cert_serial: chỉ serial mới nhất còn hiệu lực
	NotAfter time.Time
}

// IssueClient cấp chứng chỉ client cho agent từ khóa Ed25519 máy đã gửi lúc đăng ký.
// CN là mã máy (device_id) để server biết chứng chỉ thuộc máy nào.
func (ca *CA) IssueClient(pub ed25519.PublicKey, deviceID string) (Issued, error) {
	if len(pub) != ed25519.PublicKeySize {
		return Issued{}, errors.New("khóa công khai không hợp lệ")
	}
	now := time.Now()
	serial := randSerial()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: deviceID, OrganizationalUnit: []string{"scs-agent"}},
		NotBefore:    now.Add(-backdate),
		NotAfter:     now.Add(ClientValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.key)
	if err != nil {
		return Issued{}, err
	}
	return Issued{PEM: certPEM(der), Serial: SerialHex(serial), NotAfter: tmpl.NotAfter}, nil
}

// SerialHex: dạng serial lưu trong DB và so khi agent kết nối.
func SerialHex(n *big.Int) string { return hex.EncodeToString(n.Bytes()) }

func randSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err) // crypto/rand hỏng thì không thể tiếp tục an toàn
	}
	return n.Add(n, big.NewInt(1)) // không bao giờ là 0
}

func certPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func publicKeyEqual(a, b crypto.PublicKey) bool {
	type eq interface{ Equal(crypto.PublicKey) bool }
	ea, ok := a.(eq)
	return ok && ea.Equal(b)
}

func checkPrivate(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("đọc khóa CA: %w", err)
	}
	if p := st.Mode().Perm(); p&0o077 != 0 {
		return fmt.Errorf("%s đang quyền %o, phải là 600 (ai đọc được khóa CA là cấp được chứng chỉ giả cho máy)", path, p)
	}
	return nil
}

// WriteFile ghi file PEM: khóa quyền 600, chứng chỉ 644. Không ghi đè file đã có.
func WriteFile(path string, data []byte, secret bool) error {
	mode := os.FileMode(0o644)
	if secret {
		mode = 0o600
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s đã có, không ghi đè (xóa tay nếu thật sự muốn tạo lại)", path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

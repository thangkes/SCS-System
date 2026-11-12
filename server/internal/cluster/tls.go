package cluster

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
)

// SelfSignedCert: chứng chỉ TLS tự ký từ khóa node. Bên kia không kiểm tên / IP / hạn theo CA
// mà so khóa công khai với danh sách cụm, nên chứng chỉ chỉ là vỏ mang khóa.
func SelfSignedCert(priv ed25519.PrivateKey) (tls.Certificate, error) {
	pub := priv.Public().(ed25519.PublicKey)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "scs-node " + cc.NodeID(pub)},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}, nil
}

// peerKey: khóa Ed25519 trong chứng chỉ bên kia (chứng chỉ phải tự ký đúng bằng khóa đó).
func peerKey(rawCerts [][]byte) (ed25519.PublicKey, error) {
	if len(rawCerts) == 0 {
		return nil, errors.New("bên kia không gửi chứng chỉ")
	}
	c, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return nil, err
	}
	pub, ok := c.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("chứng chỉ bên kia không phải khóa Ed25519")
	}
	// Chứng chỉ phải do chính khóa đó ký: chứng minh bên kia giữ khóa riêng là việc của TLS
	// (CertificateVerify), bước này chỉ chặn chứng chỉ ghép bậy.
	if err := c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature); err != nil {
		return nil, errors.New("chứng chỉ bên kia không tự ký bằng khóa của nó")
	}
	return pub, nil
}

// ServerTLS: cấu hình TLS cổng 5222. Bắt buộc bên gọi gửi chứng chỉ (khóa node); việc khóa có
// thuộc cụm hay không kiểm ở tầng HTTP (Peer), vì node đang xin vào cụm cũng phải nói được.
func (n *Node) ServerTLS() (*tls.Config, error) {
	cert, err := SelfSignedCert(n.Key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			_, err := peerKey(raw)
			return err
		},
		NextProtos: []string{"http/1.1"},
	}, nil
}

// PeerCheck quyết định có nhận khóa node bên kia không (khi node này gọi node khác).
type PeerCheck func(pub ed25519.PublicKey) error

// MemberOnly: chỉ nhận node hợp lệ trong danh sách cụm của node này.
func (n *Node) MemberOnly() PeerCheck {
	return func(pub ed25519.PublicKey) error {
		if _, ok := n.Trusted(pub); !ok {
			return errors.New("node bên kia (" + cc.NodeID(pub) + ") không có trong danh sách cụm")
		}
		return nil
	}
}

// Pin: chỉ nhận đúng khóa want.
func Pin(want ed25519.PublicKey) PeerCheck {
	return func(pub ed25519.PublicKey) error {
		if !pub.Equal(want) {
			return errors.New("khóa node bên kia (" + cc.NodeID(pub) + ") khác khóa mong đợi")
		}
		return nil
	}
}

// Observe: nhận mọi khóa nhưng ghi lại để admin đối chiếu (chỉ dùng cho lệnh ping, không gửi
// dữ liệu gì quan trọng qua kết nối này).
func Observe(seen *ed25519.PublicKey) PeerCheck {
	return func(pub ed25519.PublicKey) error {
		*seen = pub
		return nil
	}
}

// ClientTLS: cấu hình TLS khi node này gọi node khác. Không kiểm theo CA hay tên máy mà so
// khóa công khai theo check (pinning).
func (n *Node) ClientTLS(check PeerCheck) (*tls.Config, error) {
	cert, err := SelfSignedCert(n.Key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, // thay bằng VerifyPeerCertificate bên dưới (so khóa)
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			pub, err := peerKey(raw)
			if err != nil {
				return err
			}
			return check(pub)
		},
	}, nil
}

// Peer: node gọi tới trong request (đã qua bắt tay TLS). ok = khóa đó là node hợp lệ của cụm.
func (n *Node) Peer(r *http.Request) (id string, m cc.Member, ok bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", cc.Member{}, false
	}
	pub, isEd := r.TLS.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !isEd {
		return "", cc.Member{}, false
	}
	m, ok = n.Trusted(pub)
	return cc.NodeID(pub), m, ok
}

// KeyFingerprint: mã khóa node để admin đối chiếu (SHA-256 khóa công khai, nhóm 4 ký tự).
func KeyFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:16])
	out := ""
	for i := 0; i < len(h); i += 4 {
		if i > 0 {
			out += "-"
		}
		out += h[i : i+4]
	}
	return out
}

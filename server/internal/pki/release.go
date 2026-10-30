package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// Khóa phát hành: ký các bản agent server phân phối. Tách khỏi CA để khóa CA không phải
// dùng thường xuyên. Khóa công khai (release.pub) đóng vào gói cài agent; agent chỉ chạy
// bản cập nhật có chữ ký đúng khóa này.

// NewReleaseKey sinh khóa phát hành Ed25519; trả PEM khóa riêng (PKCS#8) và khóa công khai (PKIX).
func NewReleaseKey() (keyPEM, pubPEM []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	k, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	p, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k}),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: p}), nil
}

// LoadReleaseKey đọc khóa phát hành (file quyền 600).
func LoadReleaseKey(path string) (ed25519.PrivateKey, error) {
	if err := checkPrivate(path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("đọc khóa phát hành: %w", err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("khóa phát hành không phải PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("khóa phát hành: %w", err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("khóa phát hành phải là Ed25519")
	}
	return priv, nil
}

// ParseReleasePub đọc khóa công khai phát hành (PEM PKIX) — agent dùng hàm giống hệt.
func ParseReleasePub(b []byte) (ed25519.PublicKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PUBLIC KEY" {
		return nil, errors.New("không phải khóa công khai PEM")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("khóa phát hành phải là Ed25519")
	}
	return pub, nil
}

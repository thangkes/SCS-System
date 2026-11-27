package rules

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// bundleContext tách chữ ký bộ rule khỏi chữ ký bản agent và chữ ký gói tin: server dùng chung
// khóa phát hành (agent đã có sẵn release.pub) nhưng một chữ ký không thể dùng thay cho loại kia.
const bundleContext = "scs-rules/v1\n"

// MaxBundle: kích thước tối đa một bộ rule đã đóng gói.
const MaxBundle = 1 << 20

// Bundle: bộ rule đã ký, dạng server lưu và agent tải về. Payload là JSON của RuleSet (đã bỏ
// tests và rule tắt) mã base64, nên chữ ký phủ đúng từng byte agent sẽ đọc.
type Bundle struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"` // của payload đã giải base64
	Payload string `json:"payload"`
	Sig     string `json:"sig"`
}

func signingInput(name, version, sha string) []byte {
	return []byte(bundleContext + name + "\n" + version + "\n" + sha)
}

// payload: phần agent sẽ nhận (bỏ tests, rule tắt) dạng JSON và SHA-256 của nó.
func payload(s *RuleSet) (*RuleSet, []byte, string, error) {
	if err := s.Validate(); err != nil {
		return nil, nil, "", err
	}
	out := s.ForAgent()
	if len(out.Rules) == 0 && out.LAN == nil { // bộ chỉ có sơ đồ LAN vẫn hợp lệ
		return nil, nil, "", ErrNoRules
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, nil, "", err
	}
	if len(data) > MaxBundle {
		return nil, nil, "", fmt.Errorf("bộ rule %d byte, tối đa %d", len(data), MaxBundle)
	}
	sum := sha256.Sum256(data)
	return out, data, hex.EncodeToString(sum[:]), nil
}

// Digest: SHA-256 của phần sẽ được ký (giống Bundle.SHA256 nếu ký bộ này). Dùng để so file
// nguồn với bản đã ký, và làm nội dung quyết định "ruleset.publish" của cụm server.
func Digest(s *RuleSet) (string, error) {
	_, _, sha, err := payload(s)
	return sha, err
}

// Build kiểm bộ rule, bỏ phần agent không cần và ký bằng khóa phát hành.
func Build(s *RuleSet, priv ed25519.PrivateKey) (*Bundle, error) {
	out, data, sha, err := payload(s)
	if err != nil {
		return nil, err
	}
	return &Bundle{
		Name: out.Name, Version: out.Version, SHA256: sha,
		Payload: base64.StdEncoding.EncodeToString(data),
		Sig:     base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signingInput(out.Name, out.Version, sha))),
	}, nil
}

// Open kiểm chữ ký và nội dung Bundle, trả bộ rule đã qua Validate (giống hệt kiểm ở server).
func (b *Bundle) Open(pub ed25519.PublicKey) (*RuleSet, error) {
	sig, err := base64.StdEncoding.DecodeString(b.Sig)
	if err != nil || !ed25519.Verify(pub, signingInput(b.Name, b.Version, b.SHA256), sig) {
		return nil, errors.New("chữ ký bộ rule không hợp lệ")
	}
	data, err := base64.StdEncoding.DecodeString(b.Payload)
	if err != nil || len(data) > MaxBundle {
		return nil, errors.New("payload bộ rule hỏng")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != b.SHA256 {
		return nil, errors.New("SHA-256 bộ rule không khớp")
	}
	var s RuleSet
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("đọc bộ rule: %w", err)
	}
	if s.Name != b.Name || s.Version != b.Version {
		return nil, errors.New("tên / phiên bản trong bộ rule khác phần đã ký")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

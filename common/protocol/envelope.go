// Package protocol định nghĩa gói tin giữa scs-agent và server.
//
// Chữ ký được tính trên byte gốc của payload (không ký trên JSON đã sắp
// xếp lại), nên hai bên không cần thống nhất cách serialize JSON:
//
//	wire    = {"payload": base64(payload), "sig": "ed25519:<base64>"}
//	payload = JSON của Envelope (header + body)
//	sig     = Ed25519(SignContext || payload)
package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Version là phiên bản giao thức hiện tại (trường "v").
const Version = 1

// SignContext được ghép trước payload khi ký, để chữ ký của gói tin
// không thể bị dùng lại cho mục đích khác.
const SignContext = "scs-agent/v1\n"

// MaxPayloadSize giới hạn kích thước payload chấp nhận ở cả hai phía.
const MaxPayloadSize = 1 << 20

const sigPrefix = "ed25519:"

// Loại gói tin.
const (
	TypeEnroll       = "enroll"
	TypeHeartbeat    = "heartbeat"
	TypeLogBatch     = "log_batch"
	TypeRuleAck      = "rule_ack"
	TypeUpdateReport = "update_report"
)

var (
	ErrBadSignature = errors.New("protocol: chữ ký không hợp lệ")
	ErrMalformed    = errors.New("protocol: gói tin sai định dạng")
)

// Envelope là phần header chung của mọi gói tin, body tùy theo Type.
type Envelope struct {
	V            int             `json:"v"`
	Type         string          `json:"type"`
	DeviceID     string          `json:"device_id,omitempty"` // trống với gói enroll
	Fingerprint  string          `json:"fingerprint"`
	TS           time.Time       `json:"ts"`
	Nonce        string          `json:"nonce"`
	AgentVersion string          `json:"agent_version"`
	Body         json.RawMessage `json:"body"`
}

// Signed là gói tin đi trên đường truyền.
type Signed struct {
	Payload []byte `json:"payload"` // encoding/json tự mã hóa base64
	Sig     string `json:"sig"`
}

// NewNonce sinh nonce ngẫu nhiên 16 byte dạng hex.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewEnvelope tạo envelope với body đã mã hóa JSON, nonce mới và thời gian hiện tại.
func NewEnvelope(typ, deviceID, fingerprint, agentVersion string, body any) (Envelope, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Envelope{}, err
	}
	nonce, err := NewNonce()
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		V:            Version,
		Type:         typ,
		DeviceID:     deviceID,
		Fingerprint:  fingerprint,
		TS:           time.Now().UTC().Truncate(time.Millisecond),
		Nonce:        nonce,
		AgentVersion: agentVersion,
		Body:         raw,
	}, nil
}

// Sign mã hóa envelope và ký bằng khóa riêng của máy.
func Sign(priv ed25519.PrivateKey, e Envelope) (Signed, error) {
	payload, err := json.Marshal(e)
	if err != nil {
		return Signed{}, err
	}
	if len(payload) > MaxPayloadSize {
		return Signed{}, fmt.Errorf("protocol: payload %d byte vượt giới hạn %d", len(payload), MaxPayloadSize)
	}
	sig := ed25519.Sign(priv, signingInput(payload))
	return Signed{Payload: payload, Sig: sigPrefix + base64.StdEncoding.EncodeToString(sig)}, nil
}

// ParseUnverified giải mã payload mà CHƯA kiểm chữ ký.
// Chỉ dùng để lấy device_id rồi tra public key; phải gọi Verify trước khi tin nội dung.
func (s Signed) ParseUnverified() (Envelope, error) {
	if len(s.Payload) == 0 || len(s.Payload) > MaxPayloadSize {
		return Envelope{}, ErrMalformed
	}
	var e Envelope
	if err := json.Unmarshal(s.Payload, &e); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if e.V == 0 || e.Type == "" || e.Nonce == "" || e.TS.IsZero() {
		return Envelope{}, fmt.Errorf("%w: thiếu trường bắt buộc", ErrMalformed)
	}
	return e, nil
}

// Verify kiểm chữ ký của gói tin với public key cho trước.
func (s Signed) Verify(pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return ErrBadSignature
	}
	enc, ok := strings.CutPrefix(s.Sig, sigPrefix)
	if !ok {
		return ErrBadSignature
	}
	sig, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if !ed25519.Verify(pub, signingInput(s.Payload), sig) {
		return ErrBadSignature
	}
	return nil
}

// Open kiểm chữ ký rồi giải mã payload.
func Open(s Signed, pub ed25519.PublicKey) (Envelope, error) {
	if err := s.Verify(pub); err != nil {
		return Envelope{}, err
	}
	return s.ParseUnverified()
}

// DecodeBody giải mã body của envelope vào v.
func (e Envelope) DecodeBody(v any) error {
	if err := json.Unmarshal(e.Body, v); err != nil {
		return fmt.Errorf("%w: body: %v", ErrMalformed, err)
	}
	return nil
}

// EncodePublicKey / DecodePublicKey dùng cho trường public_key trong gói enroll.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: public key", ErrMalformed)
	}
	return ed25519.PublicKey(b), nil
}

func signingInput(payload []byte) []byte {
	in := make([]byte, 0, len(SignContext)+len(payload))
	in = append(in, SignContext...)
	return append(in, payload...)
}

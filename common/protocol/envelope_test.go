package protocol

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func testKey() ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func fixedEnvelope() Envelope {
	return Envelope{
		V:            Version,
		Type:         TypeHeartbeat,
		DeviceID:     "DEV-1234-01",
		Fingerprint:  "9f2c",
		TS:           time.Date(2026, 10, 27, 14, 0, 5, 0, time.UTC),
		Nonce:        "b6c1f0a9e2d34c5b8a7f6e5d4c3b2a19",
		AgentVersion: "1.0.0",
		Body:         json.RawMessage(`{"uptime_s":18230,"rule_set_version":"2026.10.27-03","log_queue_pending":0}`),
	}
}

// Test vector cố định cho định dạng trên đường truyền. Agent viết lại
// phần ký (nếu có) phải cho ra đúng chữ ký này.
const fixedSig = "ed25519:2b4hFFmUhU/7Gmjijxinu26w+oEG5EAonC38gjam0GTCCT+DEdrgDVY3x/WAqpEUBuZDxSCPRK7okBIvMfHTCQ=="

func TestSignVector(t *testing.T) {
	s, err := Sign(testKey(), fixedEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	if s.Sig != fixedSig {
		t.Fatalf("Sig = %s\nmuốn  %s", s.Sig, fixedSig)
	}
}

func TestRoundTrip(t *testing.T) {
	priv := testKey()
	s, err := Sign(priv, fixedEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	// Đi qua JSON như trên đường truyền.
	wire, _ := json.Marshal(s)
	var got Signed
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	e, err := Open(got, priv.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	var hb HeartbeatBody
	if err := e.DecodeBody(&hb); err != nil {
		t.Fatal(err)
	}
	if e.DeviceID != "DEV-1234-01" || hb.UptimeS != 18230 {
		t.Fatalf("giải mã sai: %+v %+v", e, hb)
	}
}

func TestTamperedPayload(t *testing.T) {
	priv := testKey()
	s, _ := Sign(priv, fixedEnvelope())
	s.Payload = []byte(strings.Replace(string(s.Payload), "DEV-1234-01", "DEV-9999-01", 1))
	if _, err := Open(s, priv.Public().(ed25519.PublicKey)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("sửa payload phải bị từ chối, được %v", err)
	}
}

func TestWrongKey(t *testing.T) {
	s, _ := Sign(testKey(), fixedEnvelope())
	other, _, _ := ed25519.GenerateKey(nil)
	if err := s.Verify(other); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("sai khóa phải bị từ chối, được %v", err)
	}
}

func TestBadSigFormat(t *testing.T) {
	priv := testKey()
	s, _ := Sign(priv, fixedEnvelope())
	pub := priv.Public().(ed25519.PublicKey)
	for _, sig := range []string{"", "rsa:abc", "ed25519:!!!", "ed25519:" + strings.Repeat("A", 8)} {
		s2 := s
		s2.Sig = sig
		if err := s2.Verify(pub); !errors.Is(err, ErrBadSignature) {
			t.Errorf("sig %q phải bị từ chối", sig)
		}
	}
}

// Chữ ký Ed25519 thuần trên payload (không có SignContext) không được chấp nhận.
func TestContextRequired(t *testing.T) {
	priv := testKey()
	s, _ := Sign(priv, fixedEnvelope())
	raw := ed25519.Sign(priv, s.Payload)
	s.Sig = sigPrefix + encodeB64(raw)
	if err := s.Verify(priv.Public().(ed25519.PublicKey)); !errors.Is(err, ErrBadSignature) {
		t.Fatal("chữ ký thiếu SignContext phải bị từ chối")
	}
}

func TestParseUnverifiedRequiresFields(t *testing.T) {
	for _, p := range []string{``, `{}`, `{"v":1,"type":"heartbeat"}`, `not json`} {
		if _, err := (Signed{Payload: []byte(p)}).ParseUnverified(); !errors.Is(err, ErrMalformed) {
			t.Errorf("payload %q phải là ErrMalformed, được %v", p, err)
		}
	}
}

func TestNewEnvelope(t *testing.T) {
	e, err := NewEnvelope(TypeHeartbeat, "d1", "fp", "1.0.0", HeartbeatBody{UptimeS: 5})
	if err != nil {
		t.Fatal(err)
	}
	if e.V != Version || len(e.Nonce) != 32 || e.TS.IsZero() {
		t.Fatalf("envelope thiếu trường: %+v", e)
	}
	e2, _ := NewEnvelope(TypeHeartbeat, "d1", "fp", "1.0.0", nil)
	if e.Nonce == e2.Nonce {
		t.Fatal("nonce phải khác nhau mỗi lần")
	}
}

func TestPublicKeyEncoding(t *testing.T) {
	pub := testKey().Public().(ed25519.PublicKey)
	got, err := DecodePublicKey(EncodePublicKey(pub))
	if err != nil || !got.Equal(pub) {
		t.Fatalf("public key không khớp: %v", err)
	}
	if _, err := DecodePublicKey("AAAA"); err == nil {
		t.Fatal("key sai độ dài phải lỗi")
	}
}

func encodeB64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

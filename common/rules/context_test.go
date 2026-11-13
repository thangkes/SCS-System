package rules

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func ctxSet() *RuleSet {
	return &RuleSet{Schema: 2, Name: "ctx", Version: "1", Rules: []Rule{
		{ID: "ALL-01", Severity: "low", Source: "/var/log/auth.log", Match: `sshd`, Tags: []string{"bao-mat", "t0"}},
		{ID: "PRI-01", Severity: "high", Source: "/var/log/auth.log", Match: `sshd`,
			AppliesTo: map[string][]string{"role": {"pri", "sec"}}},
		{ID: "NEW-OS-01", Severity: "high", Source: "/var/log/auth.log", Match: `sshd-session`,
			AppliesTo: map[string][]string{"role": {"pri"}, "os": {"ubuntu-26.04"}}},
	}}
}

func TestSchemaRequiredForNewFields(t *testing.T) {
	s := ctxSet()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Schema = 0
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "schema: 2") {
		t.Fatalf("thiếu schema 2 phải báo lỗi: %v", err)
	}
	s.Schema = SchemaMax + 1
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "mới hơn") {
		t.Fatalf("schema cao hơn phải báo lỗi: %v", err)
	}
	// Bộ cũ không dùng trường mới vẫn hợp lệ với schema bỏ trống.
	if err := sshSet().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTagsAppliesToValidation(t *testing.T) {
	bad := map[string]func(r *Rule){
		"tag hoa":          func(r *Rule) { r.Tags = []string{"BaoMat"} },
		"tag trùng":        func(r *Rule) { r.Tags = []string{"a", "a"} },
		"quá nhiều tag":    func(r *Rule) { r.Tags = strings.Split("a,b,c,d,e,f,g,h,i", ",") },
		"applies_to rỗng":  func(r *Rule) { r.AppliesTo = map[string][]string{} },
		"khóa sai":         func(r *Rule) { r.AppliesTo = map[string][]string{"Role": {"pri"}} },
		"giá trị sai":      func(r *Rule) { r.AppliesTo = map[string][]string{"role": {"PRI"}} },
		"không có giá trị": func(r *Rule) { r.AppliesTo = map[string][]string{"role": {}} },
	}
	for name, mutate := range bad {
		s := ctxSet()
		mutate(&s.Rules[0])
		if err := s.Validate(); err == nil {
			t.Errorf("%s: phải báo lỗi", name)
		}
	}
}

func TestCompileForContext(t *testing.T) {
	ids := func(e *Engine) (out []string) {
		for _, rs := range e.bySource {
			for _, c := range rs {
				out = append(out, c.ID)
			}
		}
		return out
	}
	cases := []struct {
		ctx     Context
		run     int
		skipped []string
	}{
		{nil, 3, nil}, // server chạy thử: không lọc
		{Context{"role": "pri", "os": "ubuntu-26.04"}, 3, nil},
		{Context{"role": "pri", "os": "ubuntu-22.04"}, 2, []string{"NEW-OS-01"}},
		{Context{"role": "phu"}, 1, []string{"PRI-01", "NEW-OS-01"}},
		{Context{}, 1, []string{"PRI-01", "NEW-OS-01"}}, // máy không có khóa role: rule có applies_to không chạy
	}
	for _, c := range cases {
		e, err := CompileFor(ctxSet(), c.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(e); len(got) != c.run {
			t.Errorf("ngữ cảnh %v: chạy %v", c.ctx, got)
		}
		if got := e.Skipped(); strings.Join(got, ",") != strings.Join(c.skipped, ",") {
			t.Errorf("ngữ cảnh %v: bỏ %v, phải là %v", c.ctx, got, c.skipped)
		}
	}
	// Mọi rule đều không khớp ngữ cảnh.
	s := ctxSet()
	s.Rules = s.Rules[1:]
	if _, err := CompileFor(s, Context{"role": "phu"}); !errors.Is(err, ErrNoRules) {
		t.Fatal(err)
	}
}

func TestEventCarriesTags(t *testing.T) {
	e, _ := CompileFor(ctxSet(), Context{"role": "phu"})
	evs := e.Feed(Line{Source: "/var/log/auth.log", Text: "sshd[1]: x", Time: time.Now()})
	if len(evs) != 1 || strings.Join(evs[0].Tags, ",") != "bao-mat,t0" {
		t.Fatalf("%+v", evs)
	}
}

func TestParseContext(t *testing.T) {
	c, err := ParseContext(" role=pri, os=ubuntu-22.04 ")
	if err != nil || c["role"] != "pri" || c["os"] != "ubuntu-22.04" || c.String() != "os=ubuntu-22.04 role=pri" {
		t.Fatal(c, err)
	}
	if c, err := ParseContext(""); c != nil || err != nil {
		t.Fatal("rỗng = không lọc")
	}
	for _, bad := range []string{"role", "Role=pri", "role=PRI", "role=a b"} {
		if _, err := ParseContext(bad); err == nil {
			t.Errorf("%q phải lỗi", bad)
		}
	}
}

// Bộ schema 2 ký xong mở lại được, giữ tags / applies_to; trường lạ (bộ của bản sau) bị từ chối.
func TestBundleSchema2(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	b, err := Build(ctxSet(), priv)
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.Open(pub)
	if err != nil {
		t.Fatal(err)
	}
	if s.Schema != 2 || len(s.Rules[0].Tags) != 2 || len(s.Rules[2].AppliesTo) != 2 {
		t.Fatalf("%+v", s)
	}
	raw, _ := base64.StdEncoding.DecodeString(b.Payload)
	raw = bytes.Replace(raw, []byte(`"schema":2`), []byte(`"schema":2,"tuong_lai":1`), 1)
	if !json.Valid(raw) {
		t.Fatal("payload thử không hợp lệ")
	}
	nb, _ := buildRaw(raw, s.Name, s.Version, priv)
	if _, err := nb.Open(pub); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("trường lạ phải bị từ chối: %v", err)
	}
}

// buildRaw ký một payload tùy ý (giả lập bộ rule do bản server mới hơn ký).
func buildRaw(data []byte, name, version string, priv ed25519.PrivateKey) (*Bundle, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	return &Bundle{Name: name, Version: version, SHA256: sha, Payload: base64.StdEncoding.EncodeToString(data),
		Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signingInput(name, version, sha)))}, nil
}

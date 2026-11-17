package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
)

func getRules(d Deps, cert *x509.Certificate, v string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/v1/rules/"+v, nil)
	r.SetPathValue("version", v)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	w := httptest.NewRecorder()
	rulesHandler(d).ServeHTTP(w, r)
	return w
}

// Phát bộ rule (M7): admin gán bộ đã ký theo cấp; heartbeat trả bộ máy cần chạy (chỉ cho agent hiểu
// schema rule); máy chỉ tải được đúng bộ đang gán cho nó; cấp máy thắng cấp chung.
func TestRuleDistribution(t *testing.T) {
	d, reg := enrollDeps(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := rulestore.Open(t.TempDir(), pub)
	d.Rules, d.ReleaseKey = s, priv
	for _, v := range []string{"1", "2"} {
		e, _ := s.Add([]byte(ruleYAML(v)), nil)
		if _, err := s.Sign(e.File, priv, "a", 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.Add([]byte(ruleYAML("3")), nil) // chưa ký
	h := AdminHandler(d)
	assign := func(body string) int {
		code, _, _ := ruleCall(t, h, "POST", "/admin/v1/rules/assign", body, true)
		return code
	}
	if c := assign(`{"scope":"global","key":"","rule_set":"thu@3"}`); c != 409 {
		t.Fatalf("gán bộ chưa ký: %d", c)
	}
	if c := assign(`{"scope":"global","key":"","rule_set":"../x@1"}`); c != 409 && c != 400 {
		t.Fatalf("tên lạ: %d", c)
	}
	if c := assign(`{"scope":"global","key":"","rule_set":"thu@1"}`); c != 200 {
		t.Fatalf("gán chung: %d", c)
	}

	a := newTestAgent(t, "12345-TN1-PRI")
	cert, _ := approvedAgent(t, d, reg, a)
	dev := cert.Subject.CommonName
	hb := func(caps []string) protocol.Response {
		t.Helper()
		w, resp := postHeartbeat(d, cert, heartbeatPacket(t, a, dev, func(e *protocol.Envelope) {
			e.Body, _ = json.Marshal(protocol.HeartbeatBody{UptimeS: 60, Capabilities: caps})
		}))
		if w.Code != 200 {
			t.Fatalf("heartbeat: %d", w.Code)
		}
		return resp
	}
	if r := hb(nil); r.RuleSetVersion != "" {
		t.Fatalf("agent bản cũ (không báo rules/N) không được nhận bộ rule: %q", r.RuleSetVersion)
	}
	if r := hb([]string{"rules/2"}); r.RuleSetVersion != "thu@1" {
		t.Fatalf("heartbeat phải trả bộ gán chung: %q", r.RuleSetVersion)
	}
	if w := getRules(d, cert, "thu@1"); w.Code != 200 || !strings.Contains(w.Body.String(), `"sig"`) {
		t.Fatalf("tải bộ đang gán: %d %s", w.Code, w.Body)
	}
	if w := getRules(d, cert, "thu@2"); w.Code != 404 {
		t.Fatalf("bộ không gán cho máy này phải 404: %d", w.Code)
	}
	// Cấp máy thắng cấp chung.
	if c := assign(`{"scope":"device","key":"` + dev + `","rule_set":"thu@2"}`); c != 200 {
		t.Fatalf("gán cho máy: %d", c)
	}
	if r := hb([]string{"rules/2"}); r.RuleSetVersion != "thu@2" {
		t.Fatalf("cấp máy phải thắng: %q", r.RuleSetVersion)
	}
	code, out, _ := ruleCall(t, h, "GET", "/admin/v1/rules/assign", "", true)
	if code != 200 || len(out["assignments"].([]any)) != 2 {
		t.Fatalf("danh sách gán: %d %v", code, out)
	}
	// Bỏ gán cấp máy: về bộ chung.
	assign(`{"scope":"device","key":"` + dev + `","rule_set":""}`)
	if r := hb([]string{"rules/2"}); r.RuleSetVersion != "thu@1" {
		t.Fatalf("bỏ gán cấp máy: %q", r.RuleSetVersion)
	}
	// Bản ký bị xóa khỏi kho: không phát nữa (agent giữ bộ đang cài).
	if err := s.RemoveBundle("thu", "1"); err != nil {
		t.Fatal(err)
	}
	if r := hb([]string{"rules/2"}); r.RuleSetVersion != "" {
		t.Fatalf("bản ký đã xóa không được phát: %q", r.RuleSetVersion)
	}
}

package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/thangkes/SCS-System/common/rules"
)

// Server chỉ phát bộ rule agent đọc được: agent báo "rules/2" không được mời tải bộ schema 3 (rule kiểm
// thiết bị) — nếu không, agent cũ từ chối, tải lại mãi và tốn lượt tải.
func TestRuleSchemaGate(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	set := &rules.RuleSet{Schema: 3, Name: "net", Version: "1", Rules: []rules.Rule{{ID: "NET-GW-01", Severity: "high",
		Source: rules.SourceProbe, Probe: &rules.Probe{Kind: "icmp", Targets: []string{rules.TargetGateway}}, Match: `probe icmp \S+ down`}}}
	b, err := rules.Build(set, priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	if got := bundleSchema(raw); got != 3 {
		t.Fatalf("schema bộ rule: %d", got)
	}
	if bundleSchema([]byte("hỏng")) <= 3 {
		t.Fatal("bộ rule hỏng không được phát")
	}
	if capRules([]string{"rules/2"}) != 2 || capRules([]string{"x/1", "rules/3"}) != 3 || capRules(nil) != 0 {
		t.Fatal("đọc khả năng rules/<n>")
	}
}

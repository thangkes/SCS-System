package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
)

// Agent báo bộ rule đang cài trong heartbeat: trang Máy thấy bộ của từng máy, trang Rule đếm số máy
// đang dùng mỗi bộ trong kho (khớp tên + phiên bản + SHA), bộ không có trong kho liệt kê riêng.
func TestAppliedRules(t *testing.T) {
	d, reg := enrollDeps(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := rulestore.Open(t.TempDir(), pub)
	d.Rules, d.ReleaseKey = s, priv
	e, err := s.Add([]byte(ruleYAML("1")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if e, err = s.Sign(e.File, priv, "a", 0, ""); err != nil {
		t.Fatal(err)
	}

	hb := func(a *testAgent, rep *protocol.RuleSetReport) {
		t.Helper()
		cert, _ := approvedAgent(t, d, reg, a)
		w, _ := postHeartbeat(d, cert, heartbeatPacket(t, a, cert.Subject.CommonName, func(env *protocol.Envelope) {
			env.Body, _ = json.Marshal(protocol.HeartbeatBody{UptimeS: 60, Rules: rep})
		}))
		if w.Code != 200 {
			t.Fatalf("heartbeat: %d %s", w.Code, w.Body)
		}
	}
	hb(newTestAgent(t, "12345-TN1-PRI"), &protocol.RuleSetReport{Installed: true, Name: "thu", Version: "1", SHA256: e.SHA256, Total: 1, Active: 1})
	hb(newTestAgent(t, "12345-TN2-SEC"), &protocol.RuleSetReport{Installed: true, Name: "thu", Version: "1", SHA256: strings.Repeat("b", 64), Total: 1, Active: 1})
	hb(newTestAgent(t, "12345-TN3"), &protocol.RuleSetReport{})
	// Báo cáo sai định dạng bị làm sạch, không làm hỏng heartbeat.
	hb(newTestAgent(t, "12345-TN4"), &protocol.RuleSetReport{Installed: true, Name: "../x", Version: "1", SHA256: "zz", Total: -5, Error: "lỗi\x1b[31m"})

	devs, _ := reg.ListDevices(context.Background(), DeviceApproved)
	byHost := map[string]*protocol.RuleSetReport{}
	for _, dv := range devs {
		byHost[dv.Hostname] = dv.Rules
	}
	if r := byHost["12345-TN1-PRI"]; r == nil || !r.Installed || r.Name != "thu" {
		t.Fatalf("máy 1: %+v", r)
	}
	if r := byHost["12345-TN4"]; r == nil || r.Installed || r.Name != "" || r.Total != 0 || strings.ContainsRune(r.Error, 0x1b) {
		t.Fatalf("báo cáo sai phải được làm sạch: %+v", r)
	}

	code, out, _ := ruleCall(t, AdminHandler(d), "GET", "/admin/v1/rules", "", true)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	counts := out["devices"].([]any)
	if len(counts) != 1 || counts[0].(float64) != 1 {
		t.Fatalf("số máy dùng bộ trong kho: %v", out["devices"])
	}
	if other := out["other_usage"].([]any); len(other) != 1 || other[0].(map[string]any)["devices"].(float64) != 1 {
		t.Fatalf("máy chạy bản ký khác: %v", out["other_usage"])
	}
	if out["no_rules"].(float64) != 2 || out["unknown_rules"].(float64) != 0 {
		t.Fatalf("chưa cài / không báo: %v %v", out["no_rules"], out["unknown_rules"])
	}
}

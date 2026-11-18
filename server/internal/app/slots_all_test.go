package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
)

// Mọi lần tải đều phải giữ lượt: máy mới nhận chứng chỉ lúc hòa mạng, tải bộ rule, tải bản agent.
// Hết lượt: máy mới vẫn "chờ duyệt", heartbeat không báo bộ rule cần tải, tải thẳng bị 503.
func TestSlotsCoverEnrollAndRules(t *testing.T) {
	d, reg := enrollDeps(t)
	d.Downloads = NewDownloads(1, 1000)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := rulestore.Open(t.TempDir(), pub)
	d.Rules, d.ReleaseKey = s, priv
	e, _ := s.Add([]byte(ruleYAML("1")), nil)
	if _, err := s.Sign(e.File, priv, "a", 0, ""); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := ruleCall(t, AdminHandler(d), "POST", "/admin/v1/rules/assign", `{"scope":"global","key":"","rule_set":"thu@1"}`, true); c != 200 {
		t.Fatalf("gán bộ rule: %d", c)
	}
	a := newTestAgent(t, "12345-TN1-PRI")
	cert, _ := approvedAgent(t, d, reg, a)
	dev := cert.Subject.CommonName
	if st := d.Downloads.Stats(); st.InUse != 0 || st.Done != 0 {
		t.Fatalf("nhận chứng chỉ xong phải trả lượt, không tính là lượt tải: %+v", st)
	}

	// Máy mới đã được duyệt nhưng hết lượt: vẫn trả "chờ duyệt", không cấp chứng chỉ.
	ing := IngestHandler(d)
	b := newTestAgent(t, "12345-TN2-SEC")
	postEnroll(ing, b.packet(t, nil), "192.168.1.50")
	list, _ := reg.ListDevices(context.Background(), DevicePending)
	reg.Approve(context.Background(), list[0].ID, "Quản trị", "")
	d.Downloads.Acquire(999) // máy khác đang giữ lượt duy nhất
	if _, r := postEnroll(ing, b.packet(t, nil), "192.168.1.50"); r.Status != protocol.EnrollPending || r.CertificatePEM != "" {
		t.Fatalf("hết lượt: máy mới phải chờ, được %q", r.Status)
	}

	hb := func(rules *protocol.RuleSetReport) protocol.Response {
		t.Helper()
		w, resp := postHeartbeat(d, cert, heartbeatPacket(t, a, dev, func(e *protocol.Envelope) {
			e.Body, _ = json.Marshal(protocol.HeartbeatBody{UptimeS: 60, Capabilities: []string{"rules/2"}, Rules: rules})
		}))
		if w.Code != 200 {
			t.Fatalf("heartbeat: %d", w.Code)
		}
		return resp
	}
	if r := hb(&protocol.RuleSetReport{}); r.RuleSetVersion != "" {
		t.Fatalf("hết lượt: không báo bộ rule cần tải, được %q", r.RuleSetVersion)
	}
	if w := getRules(d, cert, "thu@1"); w.Code != 503 {
		t.Fatalf("tải bộ rule khi hết lượt phải 503, được %d", w.Code)
	}
	if r := hb(&protocol.RuleSetReport{Installed: true, Name: "thu", Version: "1"}); r.RuleSetVersion != "thu@1" {
		t.Fatal("máy đã cài đúng bộ: vẫn báo bộ đang gán (không cần lượt)")
	}

	d.Downloads.Free(999)
	if r := hb(&protocol.RuleSetReport{}); r.RuleSetVersion != "thu@1" {
		t.Fatal("có lượt: phải báo bộ rule")
	}
	if w := getRules(d, cert, "thu@1"); w.Code != 200 {
		t.Fatalf("giữ lượt thì tải được: %d", w.Code)
	}
	if st := d.Downloads.Stats(); st.InUse != 0 {
		t.Fatalf("tải xong bộ rule phải trả lượt: %+v", st)
	}
	if _, r := postEnroll(ing, b.packet(t, nil), "192.168.1.50"); r.Status != protocol.EnrollApproved {
		t.Fatalf("có lượt: máy mới nhận chứng chỉ, được %q", r.Status)
	}
}

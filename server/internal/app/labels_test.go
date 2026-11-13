package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/thangkes/SCS-System/common/protocol"
)

// Nhãn máy: đặt qua API theo 3 cấp, gộp theo khóa, gửi xuống agent qua heartbeat cùng cấu hình.
func TestLabelsAPIAndHeartbeat(t *testing.T) {
	d, reg := enrollDeps(t)
	a := newTestAgent(t, "12345-TN1-PRI")
	cert, _ := approvedAgent(t, d, reg, a)
	reg.find(1).StoreCode = "12345"

	// Chưa đặt gì: không gửi cấu hình.
	if _, resp := postHeartbeat(d, cert, heartbeatPacket(t, a, "1", nil)); resp.Config != nil {
		t.Fatalf("chưa đặt cấu hình thì không gửi: %+v", resp.Config)
	}

	ad, _, _ := releaseDeps(t)
	ad.Registry = reg
	h := AdminHandler(ad)
	post := func(body string) int {
		t.Helper()
		r := httptest.NewRequest("POST", "/admin/v1/agent/labels", bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for body, code := range map[string]int{
		`{"scope":"global","key":"","labels":{"vung":"mien-nam","os":"ubuntu-22.04"}}`: 200,
		`{"scope":"store","key":"12345","labels":{"vung":"mien-bac"}}`:                 200,
		`{"scope":"device","key":"1","labels":{"role":"sec"}}`:                         200,
		`{"scope":"store","key":"abc","labels":{"a":"b"}}`:                             400,
		`{"scope":"global","key":"","labels":{"Role":"pri"}}`:                          400,
		`{"scope":"global","key":"","labels":{"role":"PRI"}}`:                          400,
	} {
		if c := post(body); c != code {
			t.Errorf("%s: %d, phải là %d", body, c, code)
		}
	}
	_, resp := postHeartbeat(d, cert, heartbeatPacket(t, a, "1", nil))
	want := map[string]string{"vung": "mien-bac", "os": "ubuntu-22.04", "role": "sec"}
	if resp.Config == nil || !reflect.DeepEqual(resp.Config.Labels, want) || resp.Config.WatchServices != nil {
		t.Fatalf("nhãn gộp sai: %+v", resp.Config)
	}
	// Agent đã có bản cấu hình này: không gửi lại.
	ver := resp.Config.Version
	_, resp = postHeartbeat(d, cert, heartbeatPacket(t, a, "1", func(e *protocol.Envelope) {
		e.Body, _ = json.Marshal(protocol.HeartbeatBody{ConfigVersion: ver})
	}))
	if resp.Config != nil {
		t.Fatal("cùng phiên bản thì không gửi lại")
	}
	// Đặt dịch vụ theo dõi không làm mất nhãn, bỏ nhãn không làm mất dịch vụ.
	if err := reg.SetConfig(context.Background(), ConfigEntry{Scope: ScopeDevice, Key: "1", WatchServices: []string{"pos.service"}}); err != nil {
		t.Fatal(err)
	}
	if c := post(`{"scope":"device","key":"1","labels":null}`); c != 200 {
		t.Fatal(c)
	}
	plan, _ := reg.AgentPlan(context.Background(), 1, "")
	if plan.Labels["role"] != "" || plan.Labels["vung"] != "mien-bac" || !reflect.DeepEqual(plan.WatchServices, []string{"pos.service"}) {
		t.Fatalf("%+v", plan)
	}
}

func TestConfigOfVersion(t *testing.T) {
	// Chưa có nhãn: mã băm như bản trước (agent đang chạy không phải nhận lại).
	if a, b := configOf([]string{"a.service"}, nil), configOf([]string{"a.service"}, nil); a.Version != b.Version {
		t.Fatal("cùng nội dung phải cùng mã")
	}
	x := configOf(nil, map[string]string{"role": "pri", "os": "ubuntu-22.04"})
	y := configOf(nil, map[string]string{"os": "ubuntu-22.04", "role": "pri"})
	z := configOf(nil, map[string]string{"os": "ubuntu-22.04", "role": "sec"})
	if x.Version != y.Version || x.Version == z.Version || x.WatchServices != nil {
		t.Fatalf("%+v %+v %+v", x, y, z)
	}
}

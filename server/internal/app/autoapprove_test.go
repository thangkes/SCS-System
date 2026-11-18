package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/common/protocol"
)

// Duyệt tự động: tắt thì chờ như cũ; bật thì chỉ duyệt khi siêu thị có trong dữ liệu đã nạp và máy
// gửi từ IP WAN của siêu thị; thiếu thông tin thì chờ (kèm lý do), nạp xong thì lần hỏi sau được duyệt.
func TestAutoApprove(t *testing.T) {
	d, reg := enrollDeps(t)
	h := IngestHandler(d)
	ctx := context.Background()
	const wan = "203.0.113.10"

	a := newTestAgent(t, "12345-TN1-PRI")
	reg.auto.storeInfo = map[string][]string{"12345": {wan}}
	if _, resp := postEnroll(h, a.packet(t, nil), wan); resp.Status != protocol.EnrollPending {
		t.Fatalf("công tắc tắt phải chờ duyệt, được %q", resp.Status)
	}

	reg.SetAutoApprove(ctx, true, "Quản trị")
	if _, resp := postEnroll(h, a.packet(t, nil), wan); resp.Status != protocol.EnrollApproved || resp.CertificatePEM == "" {
		t.Fatalf("đủ điều kiện phải được duyệt và nhận chứng chỉ ngay, được %q", resp.Status)
	}
	if list, _ := reg.ListDevices(ctx, DeviceApproved); len(list) != 1 || list[0].DecidedBy != AutoApprover {
		t.Fatalf("máy phải ghi người duyệt là %q: %+v", AutoApprover, list)
	}

	// Sai IP, chưa có thông tin, hostname sai, trùng vị trí: chờ duyệt kèm lý do.
	cases := []struct{ host, ip, want string }{
		{"12345-TN2-SEC", "198.51.100.7", "không thuộc IP WAN"},
		{"67890-TN1-PRI", wan, "chưa có thông tin"},
		{"may-le", wan, "Hostname sai"},
		{"12345-TN1-PRI", wan, "Trùng hostname / vị trí TN"},
	}
	agents := map[string]*testAgent{}
	for _, c := range cases {
		x := newTestAgent(t, c.host)
		agents[c.host] = x
		if _, resp := postEnroll(h, x.packet(t, nil), c.ip); resp.Status != protocol.EnrollPending {
			t.Fatalf("%s: phải chờ duyệt, được %q", c.host, resp.Status)
		}
		list, _ := reg.ListDevices(ctx, DevicePending)
		var note string
		for _, dv := range list {
			if dv.Hostname == c.host {
				note = dv.AutoNote
			}
		}
		if !strings.Contains(note, c.want) {
			t.Fatalf("%s: lý do %q phải chứa %q", c.host, note, c.want)
		}
	}

	// Nạp thông tin siêu thị 67890 → lần hỏi lại kế tiếp được duyệt.
	reg.mu.Lock()
	reg.auto.storeInfo["67890"] = []string{"192.0.2.1", wan}
	reg.mu.Unlock()
	if _, resp := postEnroll(h, agents["67890-TN1-PRI"].packet(t, nil), wan); resp.Status != protocol.EnrollApproved {
		t.Fatalf("nạp thông tin xong, lần hỏi lại phải được duyệt; được %q", resp.Status)
	}

	// Tắt công tắc: máy đủ điều kiện cũng chờ người duyệt, lý do cũ được xóa.
	reg.SetAutoApprove(ctx, false, "Quản trị")
	if _, resp := postEnroll(h, agents["12345-TN2-SEC"].packet(t, nil), wan); resp.Status != protocol.EnrollPending {
		t.Fatalf("tắt rồi phải chờ duyệt, được %q", resp.Status)
	}
	for _, dv := range reg.devices {
		if dv.AutoNote != "" {
			t.Fatalf("tắt rồi không còn lý do: %+v", dv.Device)
		}
	}
}

// API công tắc: đọc ở mức Chỉ xem, đổi cần mức Agent; body sai bị chặn.
func TestAutoApproveAPI(t *testing.T) {
	d, reg := enrollDeps(t)
	h := AdminHandler(d)
	do := func(method, body string) (int, map[string]any) {
		r := httptest.NewRequest(method, "/admin/v1/auto-approve", bytes.NewBufferString(body))
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var j map[string]any
		json.Unmarshal(w.Body.Bytes(), &j)
		return w.Code, j
	}
	if c, j := do("GET", ""); c != 200 || j["setting"].(map[string]any)["on"] != false || len(j["rules"].([]any)) != len(AutoApproveRules) {
		t.Fatalf("GET: %d %v", c, j)
	}
	if c, _ := do("POST", `{}`); c != 400 {
		t.Fatalf("thiếu on phải 400, được %d", c)
	}
	if c, j := do("POST", `{"on":true}`); c != 200 || !reg.auto.on || j["setting"].(map[string]any)["on"] != true {
		t.Fatalf("bật: %d %v", c, j)
	}
}

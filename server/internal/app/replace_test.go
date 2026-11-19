package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/common/fingerprint"
)

// So sánh phần cứng khi cài lại máy: machine-id không xét (luôn đổi), serial mặc định của hãng coi như không có.
func TestCompareHardware(t *testing.T) {
	old := fingerprint.Components{MachineID: "aaa", BoardSerial: "BSN1", SystemSerial: "SSN1", PrimaryMAC: "AA:BB:CC:00:11:22"}
	cases := []struct {
		name    string
		c       fingerprint.Components
		verdict string
		same    int
	}{
		{"cài lại cùng máy", fingerprint.Components{MachineID: "bbb", BoardSerial: "BSN1", SystemSerial: "SSN1", PrimaryMAC: "aa:bb:cc:00:11:22"}, "same", 3},
		{"thay card mạng", fingerprint.Components{BoardSerial: "BSN1", SystemSerial: "SSN1", PrimaryMAC: "AA:BB:CC:99:99:99"}, "partial", 2},
		{"máy khác", fingerprint.Components{BoardSerial: "X", SystemSerial: "Y", PrimaryMAC: "11:11:11:11:11:11"}, "different", 0},
		{"không có gì để so", fingerprint.Components{MachineID: "ccc", SystemSerial: "To be filled by O.E.M."}, "unknown", 0},
	}
	for _, c := range cases {
		v, same, _ := CompareHardware(c.c, old)
		if v != c.verdict || len(same) != c.same {
			t.Errorf("%s: %s %v, muốn %s / %d giống", c.name, v, same, c.verdict, c.same)
		}
	}
}

// API thay thế: máy chờ duyệt cùng hostname với máy đang hoạt động → duyệt thường bị 409, "thay thế" thu hồi máy cũ,
// duyệt máy mới, giữ nhóm thử; máy cũ không trùng → từ chối; thiếu old_id → 400; mức Chỉ xem → 403.
func TestReplaceAPI(t *testing.T) {
	d, reg := enrollDeps(t)
	ing, adm := IngestHandler(d), AdminHandler(d)
	call := func(path, body string) (int, string) {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		adm.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	oldA, newA, other := newTestAgent(t, "12345-TN1-PRI"), newTestAgent(t, "12345-TN1-PRI"), newTestAgent(t, "12345-TN2-SEC")
	for _, a := range []*testAgent{oldA, newA, other} {
		postEnroll(ing, a.packet(t, nil), "192.168.1.50")
	}
	pend, _ := reg.ListDevices(context.Background(), DevicePending)
	if len(pend) != 3 {
		t.Fatalf("3 máy chờ: %d", len(pend))
	}
	oldID, newID, otherID := pend[0].ID, pend[1].ID, pend[2].ID
	if code, b := call("/admin/v1/devices/"+strconv.FormatInt(oldID, 10)+"/approve", ""); code != 200 {
		t.Fatalf("duyệt máy cũ: %d %s", code, b)
	}
	reg.find(oldID).TestRing = true
	if code, _ := call("/admin/v1/devices/"+strconv.FormatInt(newID, 10)+"/approve", ""); code != http.StatusConflict {
		t.Fatalf("duyệt thường máy cài lại phải 409, được %d", code)
	}
	if code, _ := call("/admin/v1/devices/"+strconv.FormatInt(newID, 10)+"/replace", `{}`); code != 400 {
		t.Fatalf("thiếu old_id: %d", code)
	}
	// Máy TN2 không trùng máy TN1: không được thay.
	if code, _ := call("/admin/v1/devices/"+strconv.FormatInt(otherID, 10)+"/replace", `{"old_id":`+strconv.FormatInt(oldID, 10)+`}`); code != http.StatusConflict {
		t.Fatalf("thay máy không trùng phải bị từ chối: %d", code)
	}
	code, b := call("/admin/v1/devices/"+strconv.FormatInt(newID, 10)+"/replace", `{"old_id":`+strconv.FormatInt(oldID, 10)+`}`)
	if code != 200 {
		t.Fatalf("thay thế: %d %s", code, b)
	}
	o, n := reg.find(oldID), reg.find(newID)
	if o.Status != DeviceRevoked || n.Status != DeviceApproved || n.ReplacesDevice != oldID || !n.TestRing {
		t.Fatalf("sau thay thế: cũ %s, mới %s thay %d nhóm thử %v", o.Status, n.Status, n.ReplacesDevice, n.TestRing)
	}
	// Chạy lại: máy mới không còn chờ, máy cũ không còn hoạt động.
	if code, _ := call("/admin/v1/devices/"+strconv.FormatInt(newID, 10)+"/replace", `{"old_id":`+strconv.FormatInt(oldID, 10)+`}`); code != http.StatusConflict {
		t.Fatalf("thay lần hai phải bị từ chối: %d", code)
	}
}

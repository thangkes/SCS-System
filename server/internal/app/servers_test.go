package app

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/addrbook"
)

const (
	svLAN = "https://192.168.1.10:6222"
	svNew = "https://ten.ddns.net:16222"
)

// Đổi địa chỉ lúc đang chạy: heartbeat gửi danh sách mới ngay, server đếm máy đã nhận / đang dùng.
func TestHeartbeatUsesAddressBook(t *testing.T) {
	d, reg := enrollDeps(t)
	b, err := addrbook.Open(filepath.Join(t.TempDir(), "agent-servers.json"), []string{svLAN})
	if err != nil {
		t.Fatal(err)
	}
	d.Servers = b
	a := newTestAgent(t, "12345-TN1-PRI")
	cert, enr := approvedAgent(t, d, reg, a)
	if !reflect.DeepEqual(enr.Servers, []string{svLAN}) {
		t.Fatalf("gói duyệt: %v", enr.Servers)
	}
	oldHash := protocol.ServersHash(b.URLs())
	if _, err := b.Add(svNew, "", false); err != nil {
		t.Fatal(err)
	}
	hb := func(hash, url string) protocol.Response {
		t.Helper()
		w, resp := postHeartbeat(d, cert, heartbeatPacket(t, a, "1", func(e *protocol.Envelope) {
			e.Body, _ = json.Marshal(protocol.HeartbeatBody{UptimeS: 60, ServersHash: hash, ServerURL: url})
		}))
		if w.Code != 200 {
			t.Fatalf("heartbeat: %d %s", w.Code, w.Body)
		}
		return resp
	}
	resp := hb(oldHash, svLAN)
	if !reflect.DeepEqual(resp.Servers, []string{svLAN, svNew}) {
		t.Fatalf("heartbeat phải gửi danh sách mới: %v", resp.Servers)
	}
	if st := b.Snapshot(); st.Online != 1 || st.Synced != 0 || len(st.Pending) != 1 || st.Addrs[0].Using != 1 {
		t.Fatalf("trước khi agent nhận: %+v", st)
	}
	hb(protocol.ServersHash(resp.Servers), svNew)
	if st := b.Snapshot(); st.Synced != 1 || st.Addrs[0].Using != 0 || st.Addrs[1].Using != 1 {
		t.Fatalf("sau khi agent nhận và chuyển địa chỉ: %+v", st)
	}
}

func TestServersAdminAPI(t *testing.T) {
	d, _, _ := releaseDeps(t)
	b, _ := addrbook.Open(filepath.Join(t.TempDir(), "agent-servers.json"), []string{svLAN})
	d.Servers = b
	h := AdminHandler(d)
	call := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/admin/v1/agent/servers", bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", ""); w.Code != 200 || !strings.Contains(w.Body.String(), svLAN) {
		t.Fatalf("đọc: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct { // chạy theo thứ tự: thêm trước rồi mới đưa lên
		body string
		code int
	}{
		{`{"action":"add","url":"` + svNew + `","note":"DDNS"}`, 200},
		{`{"action":"add","url":"` + svNew + `"}`, 409},
		{`{"action":"add","url":"http://x:1"}`, 400},
		{`{"action":"up","url":"` + svNew + `"}`, 200},
		{`{"action":"x","url":"` + svNew + `"}`, 400},
		{`{"action":"remove","url":"https://khong.co:1"}`, 404},
	} {
		if w := call("POST", c.body); w.Code != c.code {
			t.Errorf("%s: %d, phải là %d (%s)", c.body, w.Code, c.code, w.Body)
		}
	}
	if got := b.URLs(); !reflect.DeepEqual(got, []string{svNew, svLAN}) {
		t.Fatalf("thứ tự sau khi đưa lên: %v", got)
	}
	// Còn máy đang dùng địa chỉ: phải xác nhận gỡ (force).
	b.Report(7, "", svLAN)
	if w := call("POST", `{"action":"remove","url":"`+svLAN+`"}`); w.Code != 409 || !strings.Contains(w.Body.String(), `"using":1`) {
		t.Fatalf("gỡ khi còn máy dùng: %d %s", w.Code, w.Body)
	}
	if w := call("POST", `{"action":"remove","url":"`+svLAN+`","force":true}`); w.Code != 200 {
		t.Fatalf("gỡ có xác nhận: %d %s", w.Code, w.Body)
	}
	if w := call("POST", `{"action":"remove","url":"`+svNew+`"}`); w.Code != 409 {
		t.Fatalf("không được gỡ địa chỉ cuối: %d %s", w.Code, w.Body)
	}
}

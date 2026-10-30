package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/fingerprint"
	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/pki"
)

type testAgent struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	comp fingerprint.Components
	host string
}

func newTestAgent(t *testing.T, host string) *testAgent {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	return &testAgent{pub: pub, priv: priv, host: host, comp: fingerprint.Components{
		MachineID: "0123456789abcdef0123456789abcdef", BoardSerial: "BSN1", PrimaryMAC: "aa:bb:cc:00:11:22"}}
}

// packet tạo gói enroll đã ký; mutate sửa envelope trước khi ký.
func (a *testAgent) packet(t *testing.T, mutate func(*protocol.Envelope)) []byte {
	t.Helper()
	env, err := protocol.NewEnvelope(protocol.TypeEnroll, "", a.comp.Hash(), "0.1.0", protocol.EnrollBody{
		PublicKey: protocol.EncodePublicKey(a.pub), Components: a.comp, Hostname: a.host, OSVersion: "Ubuntu 22.04.4 LTS"})
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&env)
	}
	s, err := protocol.Sign(a.priv, env)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	return b
}

func postEnroll(h http.Handler, body []byte, ip string) (*httptest.ResponseRecorder, protocol.EnrollResponse) {
	r := httptest.NewRequest("POST", "/v1/enroll", bytes.NewReader(body))
	r.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var resp protocol.EnrollResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func enrollDeps(t *testing.T) (Deps, *fakeRegistry) {
	d, _ := deps(t, nil)
	reg := newFakeRegistry()
	ca, _, err := pki.NewCA("test CA")
	if err != nil {
		t.Fatal(err)
	}
	d.Registry, d.CA = reg, ca
	return d, reg
}

// Luồng chính: đăng ký → chờ duyệt (hỏi lại vẫn chờ) → admin duyệt → hỏi lại nhận chứng chỉ.
func TestEnrollFlow(t *testing.T) {
	d, reg := enrollDeps(t)
	h := IngestHandler(d)
	a := newTestAgent(t, "12345-TN1-PRI")

	for i := 0; i < 2; i++ {
		w, resp := postEnroll(h, a.packet(t, nil), "203.0.113.10")
		if w.Code != 200 || resp.Status != protocol.EnrollPending || resp.CertificatePEM != "" {
			t.Fatalf("lần %d: %d %s", i, w.Code, w.Body)
		}
	}
	list, _ := reg.ListDevices(context.Background(), DevicePending)
	if len(list) != 1 || list[0].StoreCode != "12345" || list[0].TN != 1 || list[0].Role != "PRI" ||
		list[0].KeyID != KeyID(a.pub) || list[0].EnrollIP != "203.0.113.10" {
		t.Fatalf("hỏi lại không được tạo yêu cầu thứ hai; hồ sơ = %+v", list)
	}

	if _, err := reg.Approve(context.Background(), list[0].ID, "Quản trị", ""); err != nil {
		t.Fatal(err)
	}
	w, resp := postEnroll(h, a.packet(t, nil), "203.0.113.10")
	if w.Code != 200 || resp.Status != protocol.EnrollApproved || resp.DeviceID != "1" {
		t.Fatalf("sau duyệt: %d %s", w.Code, w.Body)
	}
	blk, _ := pem.Decode([]byte(resp.CertificatePEM))
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(d.CA.Cert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("chứng chỉ phải do CA ký: %v", err)
	}
	if !a.pub.Equal(cert.PublicKey) || cert.Subject.CommonName != "1" || reg.serials[1] != pki.SerialHex(cert.SerialNumber) {
		t.Fatal("chứng chỉ phải mang khóa của máy, mã máy; serial phải được lưu")
	}
	if resp.CAPEM != string(d.CA.CertPEM) {
		t.Fatal("phải trả kèm CA")
	}

	// Thu hồi → hỏi lại bị từ chối.
	reg.Revoke(context.Background(), 1, "Quản trị")
	if _, resp := postEnroll(h, a.packet(t, nil), "203.0.113.10"); resp.Status != protocol.EnrollRejected {
		t.Fatalf("máy bị thu hồi phải nhận rejected, được %q", resp.Status)
	}
}

func TestEnrollRejectsBadPackets(t *testing.T) {
	d, reg := enrollDeps(t)
	h := IngestHandler(d)
	a := newTestAgent(t, "12345-TN3")
	other := newTestAgent(t, "12345-TN4")

	tampered := a.packet(t, nil)
	tampered = bytes.Replace(tampered, []byte(`"sig":"ed25519:`), []byte(`"sig":"ed25519:A`), 1)
	signedByOther := func() []byte {
		// Gói ghi khóa của a nhưng ký bằng khóa của máy khác.
		env, _ := protocol.NewEnvelope(protocol.TypeEnroll, "", a.comp.Hash(), "0.1.0", protocol.EnrollBody{
			PublicKey: protocol.EncodePublicKey(a.pub), Components: a.comp, Hostname: a.host})
		s, _ := protocol.Sign(other.priv, env)
		b, _ := json.Marshal(s)
		return b
	}()
	cases := map[string]struct {
		body []byte
		code int
	}{
		"không phải JSON":        {[]byte("rac"), 400},
		"sai chữ ký":             {tampered, 401},
		"ký bằng khóa khác":      {signedByOther, 401},
		"giờ lệch 10 phút":       {a.packet(t, func(e *protocol.Envelope) { e.TS = e.TS.Add(-10 * time.Minute) }), 400},
		"giờ ở tương lai":        {a.packet(t, func(e *protocol.Envelope) { e.TS = e.TS.Add(10 * time.Minute) }), 400},
		"sai loại gói":           {a.packet(t, func(e *protocol.Envelope) { e.Type = protocol.TypeHeartbeat }), 400},
		"sai phiên bản":          {a.packet(t, func(e *protocol.Envelope) { e.V = 2 }), 400},
		"fingerprint không khớp": {a.packet(t, func(e *protocol.Envelope) { e.Fingerprint = strings.Repeat("0", 64) }), 400},
	}
	for name, c := range cases {
		if w, _ := postEnroll(h, c.body, "198.51.100."+string(rune('1'+len(name)%9))); w.Code != c.code {
			t.Errorf("%s: %d, muốn %d (%s)", name, w.Code, c.code, w.Body)
		}
	}
	if list, _ := reg.ListDevices(context.Background(), DevicePending); len(list) != 0 {
		t.Fatalf("gói lỗi không được tạo yêu cầu nào, có %d", len(list))
	}
}

// Hostname sai quy ước: vẫn vào hàng chờ, ghi lý do; duyệt phải nhập mã siêu thị.
func TestEnrollInvalidHostname(t *testing.T) {
	d, reg := enrollDeps(t)
	h := IngestHandler(d)
	a := newTestAgent(t, "ubuntu-desktop")
	if w, resp := postEnroll(h, a.packet(t, nil), "203.0.113.20"); w.Code != 200 || resp.Status != protocol.EnrollPending {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	list, _ := reg.ListDevices(context.Background(), DevicePending)
	if len(list) != 1 || list[0].HostnameOK || !strings.Contains(list[0].HostnameError, "không đúng mẫu") {
		t.Fatalf("hồ sơ = %+v", list)
	}
	// Tên có ký tự điều khiển: từ chối hẳn.
	bad := newTestAgent(t, "12345-TN3\x1b[2J")
	if w, _ := postEnroll(h, bad.packet(t, nil), "203.0.113.21"); w.Code != 400 {
		t.Fatalf("hostname có ký tự điều khiển phải 400, được %d", w.Code)
	}
}

func TestEnrollRateLimitAndNoCA(t *testing.T) {
	d, reg := enrollDeps(t)
	h := IngestHandler(d)
	a := newTestAgent(t, "12345-TN3")
	codes := map[int]int{}
	for i := 0; i < enrollBurst+5; i++ {
		w, _ := postEnroll(h, a.packet(t, nil), "203.0.113.30")
		codes[w.Code]++
	}
	if codes[200] != enrollBurst || codes[429] != 5 {
		t.Fatalf("một IP chỉ được %d lần liền: %v", enrollBurst, codes)
	}
	if w, _ := postEnroll(h, a.packet(t, nil), "203.0.113.31"); w.Code != 200 {
		t.Fatal("IP khác không bị ảnh hưởng")
	}

	// Đã duyệt nhưng server chưa có CA: báo rõ, không cấp chứng chỉ rỗng.
	reg.Approve(context.Background(), 1, "Quản trị", "")
	d.CA = nil
	if w, _ := postEnroll(IngestHandler(d), a.packet(t, nil), "203.0.113.32"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("không có CA phải 503, được %d", w.Code)
	}
}

func TestEnrollLimiterRefills(t *testing.T) {
	now := time.Unix(0, 0)
	l := newEnrollLimiter(func() time.Time { return now })
	ip := remoteAddr(httptest.NewRequest("GET", "/", nil))
	for i := 0; i < enrollBurst; i++ {
		l.allow(ip)
	}
	if l.allow(ip) {
		t.Fatal("hết lượt phải chặn")
	}
	now = now.Add(enrollEvery)
	if !l.allow(ip) {
		t.Fatal("sau enrollEvery phải có thêm một lượt")
	}
}

// API duyệt trên web: danh sách chờ, duyệt (hostname sai phải nhập mã siêu thị), trùng vị trí,
// từ chối, thu hồi.
func TestDeviceAPI(t *testing.T) {
	d, _ := enrollDeps(t)
	ing, adm := IngestHandler(d), AdminHandler(d)
	call := func(method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		adm.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	tn1 := newTestAgent(t, "12345-TN1-PRI")
	clone := newTestAgent(t, "12345-TN1-PRI") // máy thứ hai xưng cùng vị trí
	stray := newTestAgent(t, "ubuntu-desktop")
	for i, a := range []*testAgent{tn1, clone, stray} {
		postEnroll(ing, a.packet(t, nil), "203.0.113."+string(rune('1'+i)))
	}

	code, body := call("GET", "/admin/v1/devices", "")
	if code != 200 || strings.Count(body, `"status":"pending"`) != 3 || !strings.Contains(body, KeyID(tn1.pub)) {
		t.Fatalf("danh sách chờ: %d %s", code, body)
	}
	if code, _ := call("GET", "/admin/v1/devices?status=lung-tung", ""); code != 400 {
		t.Fatalf("status lạ phải 400, được %d", code)
	}
	if code, body := call("POST", "/admin/v1/devices/1/approve", ""); code != 200 || !strings.Contains(body, `"status":"approved"`) {
		t.Fatalf("duyệt TN1: %d %s", code, body)
	}
	if code, body := call("POST", "/admin/v1/devices/2/approve", ""); code != 409 || !strings.Contains(body, "vị trí") {
		t.Fatalf("máy thứ hai cùng vị trí phải 409: %d %s", code, body)
	}
	if code, body := call("POST", "/admin/v1/devices/3/approve", ""); code != 400 || !strings.Contains(body, "mã siêu thị") {
		t.Fatalf("hostname sai mà không nhập mã siêu thị phải 400: %d %s", code, body)
	}
	if code, _ := call("POST", "/admin/v1/devices/3/approve", `{"store_code":"ST1"}`); code != 400 {
		t.Fatalf("mã siêu thị có chữ phải 400, được %d", code)
	}
	if code, body := call("POST", "/admin/v1/devices/3/approve", `{"store_code":"12345"}`); code != 200 || !strings.Contains(body, `"store_code":"12345"`) {
		t.Fatalf("duyệt kèm mã siêu thị: %d %s", code, body)
	}
	if code, _ := call("POST", "/admin/v1/devices/2/reject", ""); code != 200 {
		t.Fatalf("từ chối máy trùng: %d", code)
	}
	if code, _ := call("POST", "/admin/v1/devices/2/reject", ""); code != 409 {
		t.Fatalf("từ chối lần hai phải 409, được %d", code)
	}
	if code, _ := call("POST", "/admin/v1/devices/1/revoke", ""); code != 200 {
		t.Fatalf("thu hồi: %d", code)
	}
	if code, _ := call("POST", "/admin/v1/devices/abc/approve", ""); code != 400 {
		t.Fatalf("mã máy không phải số phải 400, được %d", code)
	}
	if code, body := call("GET", "/admin/v1/devices?status=approved", ""); code != 200 || !strings.Contains(body, `"store_wan_ip":"203.0.113.1"`) {
		t.Fatalf("siêu thị phải nhận IP WAN của máy đầu tiên được duyệt: %s", body)
	}
}

package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

// approvedAgent: đăng ký, duyệt, nhận chứng chỉ; trả chứng chỉ đã cấp.
func approvedAgent(t *testing.T, d Deps, reg *fakeRegistry, a *testAgent) (*x509.Certificate, protocol.EnrollResponse) {
	t.Helper()
	h := IngestHandler(d)
	postEnroll(h, a.packet(t, nil), "192.168.1.50")
	list, _ := reg.ListDevices(context.Background(), DevicePending)
	if _, err := reg.Approve(context.Background(), list[len(list)-1].ID, "Quản trị", ""); err != nil {
		t.Fatal(err)
	}
	_, resp := postEnroll(h, a.packet(t, nil), "192.168.1.50")
	blk, _ := pem.Decode([]byte(resp.CertificatePEM))
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert, resp
}

func heartbeatPacket(t *testing.T, a *testAgent, deviceID string, mutate func(*protocol.Envelope)) []byte {
	t.Helper()
	env, err := protocol.NewEnvelope(protocol.TypeHeartbeat, deviceID, a.comp.Hash(), "0.2.0", protocol.HeartbeatBody{UptimeS: 60})
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&env)
	}
	s, _ := protocol.Sign(a.priv, env)
	b, _ := json.Marshal(s)
	return b
}

func postHeartbeat(d Deps, cert *x509.Certificate, body []byte) (*httptest.ResponseRecorder, protocol.Response) {
	r := httptest.NewRequest("POST", "/v1/heartbeat", bytes.NewReader(body))
	r.RemoteAddr = "192.168.1.50:40000"
	if cert != nil {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	}
	w := httptest.NewRecorder()
	heartbeatHandler(d).ServeHTTP(w, r)
	var resp protocol.Response
	json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func TestHeartbeatAndServers(t *testing.T) {
	d, reg := enrollDeps(t)
	servers := []string{"https://192.168.1.10:6222", "https://ten.ddns.net:16222"}
	d.Cfg.AgentServers = servers
	a := newTestAgent(t, "12345-TN1-PRI")
	cert, enr := approvedAgent(t, d, reg, a)
	if !reflect.DeepEqual(enr.Servers, servers) {
		t.Fatalf("gói duyệt phải kèm danh sách server, được %v", enr.Servers)
	}

	w, resp := postHeartbeat(d, cert, heartbeatPacket(t, a, "1", nil))
	if w.Code != 200 || !resp.OK || resp.NextPollS != 300 || !reflect.DeepEqual(resp.Servers, servers) {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body)
	}
	if dv := reg.find(1); dv.AgentVersion != "0.2.0" || dv.LastIP != "192.168.1.50" || time.Since(dv.LastSeen) > time.Minute {
		t.Fatalf("phải ghi lần cuối thấy: %+v", dv.Device)
	}

	other := newTestAgent(t, "12345-TN2-SEC")
	cases := map[string]struct {
		cert *x509.Certificate
		body []byte
		code int
	}{
		"không chứng chỉ": {nil, heartbeatPacket(t, a, "1", nil), 401},
		"sai mã máy":      {cert, heartbeatPacket(t, a, "2", nil), 403},
		"khóa khác ký":    {cert, heartbeatPacket(t, other, "1", nil), 401},
		"giờ lệch":        {cert, heartbeatPacket(t, a, "1", func(e *protocol.Envelope) { e.TS = time.Now().Add(-time.Hour) }), 400},
		"sai loại gói":    {cert, heartbeatPacket(t, a, "1", func(e *protocol.Envelope) { e.Type = protocol.TypeEnroll }), 400},
	}
	for name, c := range cases {
		if w, _ := postHeartbeat(d, c.cert, c.body); w.Code != c.code {
			t.Errorf("%s: mã %d, phải là %d (%s)", name, w.Code, c.code, w.Body)
		}
	}

	// Cấp lại chứng chỉ (agent hỏi lại enroll) → chứng chỉ cũ hết hiệu lực.
	cert2, _ := approvedAgent(t, d, reg, newTestAgent(t, "12345-TN3"))
	_ = cert2
	_, again := postEnroll(IngestHandler(d), a.packet(t, nil), "192.168.1.50")
	blk, _ := pem.Decode([]byte(again.CertificatePEM))
	newCert, _ := x509.ParseCertificate(blk.Bytes)
	if w, _ := postHeartbeat(d, cert, heartbeatPacket(t, a, "1", nil)); w.Code != 403 {
		t.Fatalf("chứng chỉ cũ phải bị từ chối, được %d", w.Code)
	}
	if w, _ := postHeartbeat(d, newCert, heartbeatPacket(t, a, "1", nil)); w.Code != 200 {
		t.Fatalf("chứng chỉ mới phải được nhận, được %d %s", w.Code, w.Body)
	}

	// Thu hồi → 403.
	reg.Revoke(context.Background(), 1, "Quản trị")
	if w, _ := postHeartbeat(d, newCert, heartbeatPacket(t, a, "1", nil)); w.Code != 403 {
		t.Fatalf("máy bị thu hồi phải nhận 403, được %d", w.Code)
	}
}

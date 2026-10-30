package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/common/protocol"
)

// releaseDeps: deps có khóa phát hành, thư mục bản agent đi kèm và kho phát hành tạm.
func releaseDeps(t *testing.T) (Deps, *fakeRegistry, ed25519.PublicKey) {
	t.Helper()
	d, reg := enrollDeps(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	d.ReleaseKey = priv
	d.Cfg.AgentDist = t.TempDir()
	d.Cfg.AgentReleases = t.TempDir()
	return d, reg, pub
}

func putDist(t *testing.T, dir, version string, bins map[string]string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "VERSION"), []byte(version+"\n"), 0o644)
	for arch, content := range bins {
		os.WriteFile(filepath.Join(dir, "scs-agent-linux-"+arch), []byte(content), 0o755)
	}
}

func heartbeatWith(t *testing.T, a *testAgent, version string, body protocol.HeartbeatBody) []byte {
	t.Helper()
	env, err := protocol.NewEnvelope(protocol.TypeHeartbeat, "1", a.comp.Hash(), version, body)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := protocol.Sign(a.priv, env)
	b, _ := json.Marshal(s)
	return b
}

// Luồng chính: server nhập bản agent đi kèm → admin phát hành cho nhóm thử → máy nhóm thử nhận
// bản mới qua heartbeat, tải về, chữ ký đúng; máy ngoài nhóm thử chỉ nhận khi phát hành cho tất cả.
func TestReleaseRolloutFlow(t *testing.T) {
	d, reg, pub := releaseDeps(t)
	putDist(t, d.Cfg.AgentDist, "v1.1.0", map[string]string{"amd64": "agent amd64 v1.1.0", "arm64": "agent arm64"})
	if n, err := importDist(context.Background(), d); err != nil || n != 2 {
		t.Fatalf("nhập bản agent: n=%d err=%v", n, err)
	}
	if n, _ := importDist(context.Background(), d); n != 0 {
		t.Fatal("nhập lại cùng phiên bản không được thêm bản mới")
	}

	a := newTestAgent(t, "12345-TN1-PRI")
	cert, _ := approvedAgent(t, d, reg, a)
	hb := func(version string) protocol.Response {
		t.Helper()
		w, resp := postHeartbeat(d, cert, heartbeatWith(t, a, version, protocol.HeartbeatBody{Arch: "amd64"}))
		if w.Code != 200 {
			t.Fatalf("heartbeat: %d %s", w.Code, w.Body)
		}
		return resp
	}
	if resp := hb("v1.0.0"); resp.Update != nil {
		t.Fatal("chưa phát hành thì không gửi bản mới")
	}

	reg.SetRollout(context.Background(), RingTest, "v1.1.0", "Quản trị")
	if resp := hb("v1.0.0"); resp.Update != nil {
		t.Fatal("máy ngoài nhóm thử không nhận bản đang thử")
	}
	reg.SetTestRing(context.Background(), 1, true, "Quản trị")
	resp := hb("v1.0.0")
	if resp.Update == nil || resp.Update.Version != "v1.1.0" || resp.Update.Arch != "amd64" {
		t.Fatalf("máy nhóm thử phải nhận v1.1.0, được %+v", resp.Update)
	}
	if resp := hb("v1.1.0"); resp.Update != nil {
		t.Fatal("đang chạy đúng bản thì không gửi lại")
	}

	// Tải bản về và kiểm chữ ký như agent làm.
	r := httptest.NewRequest("GET", "/v1/updates/v1.1.0?arch=amd64", nil)
	r.SetPathValue("version", "v1.1.0")
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	w := httptest.NewRecorder()
	updateHandler(d).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("tải bản: %d %s", w.Code, w.Body)
	}
	if err := protocol.VerifyRelease(pub, *resp.Update, w.Body.Bytes()); err != nil {
		t.Fatalf("bản tải về phải đúng chữ ký: %v", err)
	}
	// Không có chứng chỉ / phiên bản lạ / đường dẫn lạ.
	for path, code := range map[string]int{
		"/v1/updates/v9.9.9?arch=amd64": 404, "/v1/updates/v1.1.0?arch=mips": 400, "/v1/updates/..?arch=amd64": 400,
	} {
		r := httptest.NewRequest("GET", "/x", nil)
		r.SetPathValue("version", strings.TrimPrefix(strings.Split(path, "?")[0], "/v1/updates/"))
		r.URL.RawQuery = strings.Split(path, "?")[1]
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
		w := httptest.NewRecorder()
		updateHandler(d).ServeHTTP(w, r)
		if w.Code != code {
			t.Errorf("%s: mã %d, phải là %d", path, w.Code, code)
		}
	}
	w = httptest.NewRecorder()
	noCert := httptest.NewRequest("GET", "/v1/updates/v1.1.0?arch=amd64", nil)
	noCert.SetPathValue("version", "v1.1.0")
	updateHandler(d).ServeHTTP(w, noCert)
	if w.Code != 401 {
		t.Fatalf("không chứng chỉ phải 401, được %d", w.Code)
	}

	// Ra khỏi nhóm thử, phát hành cho tất cả.
	reg.SetTestRing(context.Background(), 1, false, "Quản trị")
	reg.SetRollout(context.Background(), RingAll, "v1.1.0", "Quản trị")
	if resp := hb("v1.0.0"); resp.Update == nil {
		t.Fatal("phát hành cho tất cả thì mọi máy nhận")
	}
	if err := reg.SetRollout(context.Background(), RingAll, "v7", "Quản trị"); err == nil {
		t.Fatal("không phát hành được bản không có trong kho")
	}
}

// Cấu hình: máy > siêu thị > chung; chỉ gửi khi khác bản agent đang dùng; chưa đặt thì không gửi.
func TestAgentConfigPush(t *testing.T) {
	d, reg, _ := releaseDeps(t)
	a := newTestAgent(t, "12345-TN1-PRI")
	cert, _ := approvedAgent(t, d, reg, a)
	hb := func(cfgVer string) *protocol.AgentConfig {
		t.Helper()
		_, resp := postHeartbeat(d, cert, heartbeatWith(t, a, "v1", protocol.HeartbeatBody{Arch: "amd64", ConfigVersion: cfgVer}))
		return resp.Config
	}
	if hb("") != nil {
		t.Fatal("chưa đặt cấu hình thì không gửi")
	}
	ctx := context.Background()
	reg.SetConfig(ctx, ConfigEntry{Scope: ScopeGlobal, WatchServices: []string{"pos.service"}, UpdatedBy: "x"})
	c := hb("")
	if c == nil || len(c.WatchServices) != 1 || c.WatchServices[0] != "pos.service" {
		t.Fatalf("phải nhận cấu hình chung, được %+v", c)
	}
	if hb(c.Version) != nil {
		t.Fatal("đã đúng phiên bản cấu hình thì không gửi lại")
	}
	reg.SetConfig(ctx, ConfigEntry{Scope: ScopeStore, Key: "12345", WatchServices: []string{"pos.service", "printer.service"}, UpdatedBy: "x"})
	if c := hb(c.Version); c == nil || len(c.WatchServices) != 2 {
		t.Fatalf("cấu hình siêu thị phải thắng cấu hình chung: %+v", c)
	}
	reg.SetConfig(ctx, ConfigEntry{Scope: ScopeDevice, Key: "1", WatchServices: []string{}, UpdatedBy: "x"})
	if c := hb(""); c == nil || len(c.WatchServices) != 0 {
		t.Fatalf("cấu hình máy (rỗng) phải thắng: %+v", c)
	}
}

func TestAgentAdminAPI(t *testing.T) {
	d, reg, _ := releaseDeps(t)
	putDist(t, d.Cfg.AgentDist, "v2.0.0", map[string]string{"amd64": "x"})
	importDist(context.Background(), d)
	h := AdminHandler(d)
	call := func(method, path, body string, role string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "/admin/v1/agent/releases", "", "")
	var got struct {
		Releases []releaseItem `json:"releases"`
		Signing  bool          `json:"signing"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || len(got.Releases) != 1 || got.Releases[0].Version != "v2.0.0" || !got.Signing {
		t.Fatalf("danh sách bản: %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/admin/v1/agent/rollout", `{"ring":"all","version":"v2.0.0"}`, ""); w.Code != 200 {
		t.Fatalf("phát hành: %d %s", w.Code, w.Body)
	}
	if ro, _ := reg.GetRollout(context.Background()); ro.All != "v2.0.0" {
		t.Fatal("phải lưu bản phát hành")
	}
	for body, code := range map[string]int{
		`{"ring":"x","version":"v2.0.0"}`: 400, `{"ring":"all","version":"v9"}`: 404, `{"ring":"all","version":"../x"}`: 400,
	} {
		if w := call("POST", "/admin/v1/agent/rollout", body, ""); w.Code != code {
			t.Errorf("rollout %s: %d, phải là %d", body, w.Code, code)
		}
	}
	for body, code := range map[string]int{
		`{"scope":"global","key":"","watch_services":["pos.service"]}`: 200,
		`{"scope":"store","key":"12345","watch_services":null}`:        200,
		`{"scope":"store","key":"abc","watch_services":[]}`:            400,
		`{"scope":"global","key":"","watch_services":["-bad"]}`:        400,
		`{"scope":"device","key":"0","watch_services":[]}`:             400,
	} {
		if w := call("POST", "/admin/v1/agent/config", body, ""); w.Code != code {
			t.Errorf("config %s: %d, phải là %d (%s)", body, w.Code, code, w.Body)
		}
	}
	if w := call("GET", "/admin/v1/agent/config", "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "pos.service") {
		t.Fatalf("đọc cấu hình: %d %s", w.Code, w.Body)
	}
}

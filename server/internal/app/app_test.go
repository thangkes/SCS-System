package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thangkes/SCS-System/server/internal/auth"
	"github.com/thangkes/SCS-System/server/internal/config"
	"github.com/thangkes/SCS-System/server/internal/httpx"
)

const testToken = "0123456789abcdef0123456789abcdef"

func deps(t *testing.T, mutate func(*config.Config)) (Deps, *bytes.Buffer) {
	t.Helper()
	c := &config.Config{Env: "dev", LogLevel: "debug", DevInsecure: true}
	if mutate != nil {
		mutate(c)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	return Deps{Cfg: c, Log: log, Version: "test", Auth: auth.NewManager(testToken, log), Registry: newFakeRegistry()}, &buf
}

func do(t *testing.T, h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	d, _ := deps(t, nil)
	for role, h := range map[string]http.Handler{
		"ingest": IngestHandler(d), "admin": AdminHandler(d),
	} {
		rec := do(t, h, "GET", "/healthz", nil)
		if rec.Code != 200 {
			t.Fatalf("%s /healthz = %d", role, rec.Code)
		}
		var body map[string]string
		json.Unmarshal(rec.Body.Bytes(), &body)
		wantVer := map[string]string{"ingest": "", "admin": "test"}[role] // 6222 công khai: không lộ phiên bản
		if body["status"] != "ok" || body["role"] != role || body["version"] != wantVer {
			t.Errorf("%s /healthz body = %v", role, body)
		}
		if rec.Header().Get("X-Request-Id") == "" {
			t.Errorf("%s thiếu X-Request-Id", role)
		}
		if rec := do(t, h, "GET", "/readyz", nil); rec.Code != 200 {
			t.Errorf("%s /readyz = %d", role, rec.Code)
		}
	}
}

func TestPlaceholderRoutes(t *testing.T) {
	d, _ := deps(t, nil)
	cases := []struct {
		h            http.Handler
		method, path string
	}{
		{IngestHandler(d), "POST", "/v1/logs"},
		{IngestHandler(d), "GET", "/v1/rules/2026.10.27-03"},
	}
	for _, c := range cases {
		if rec := do(t, c.h, c.method, c.path, nil); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, muốn 501", c.method, c.path, rec.Code)
		}
	}
	// Sai method / sai đường dẫn.
	if rec := do(t, IngestHandler(d), "GET", "/v1/logs", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/logs = %d, muốn 405", rec.Code)
	}
	if rec := do(t, IngestHandler(d), "GET", "/khong-co", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/khong-co = %d, muốn 404", rec.Code)
	}
}

func TestClientCertRequiredWhenCAConfigured(t *testing.T) {
	d, _ := deps(t, func(c *config.Config) { c.TLSClientCA = "/etc/scs/ca.pem" })
	h := IngestHandler(d)

	// Không có TLS / không có chứng chỉ client → 401.
	if rec := do(t, h, "POST", "/v1/logs", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("không chứng chỉ = %d, muốn 401", rec.Code)
	}
	// Có chứng chỉ đã kiểm → qua middleware, tới handler 501.
	req := httptest.NewRequest("POST", "/v1/logs", nil)
	req.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("có chứng chỉ = %d, muốn 501", rec.Code)
	}
	// /healthz không cần chứng chỉ (load balancer gọi).
	if rec := do(t, h, "GET", "/healthz", nil); rec.Code != 200 {
		t.Fatalf("/healthz = %d", rec.Code)
	}
}

// Thiếu CA mà không được phép rõ ràng (quên SCS_ENV=prod, nghe mọi card mạng) → đóng, không mở.
func TestNoCAFailsClosed(t *testing.T) {
	cases := map[string]func(*config.Config){
		"dev nghe mọi card mạng": func(c *config.Config) { c.DevInsecure = false; c.IngestListen = ":6222" },
		"prod":                   func(c *config.Config) { c.Env = "prod"; c.IngestListen = "127.0.0.1:6222" },
	}
	for name, mutate := range cases {
		d, _ := deps(t, mutate)
		if rec := do(t, IngestHandler(d), "POST", "/v1/logs", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: không CA = %d, muốn 401", name, rec.Code)
		}
	}
	d, _ := deps(t, func(c *config.Config) { c.DevInsecure = false; c.IngestListen = "127.0.0.1:6222" })
	if rec := do(t, IngestHandler(d), "POST", "/v1/logs", nil); rec.Code != http.StatusNotImplemented {
		t.Errorf("dev chỉ nghe loopback được thử không CA: %d", rec.Code)
	}
}

// Route quản trị: chưa đăng nhập 401; Chỉ xem đọc được nhưng ghi 403; Admin (mã hoặc Bearer) ghi được.
func TestAdminRoutesRequireLogin(t *testing.T) {
	d, logs := deps(t, nil)
	h := AdminHandler(d)
	origin := map[string]string{"Origin": "http://example.com"}

	if rec := do(t, h, "GET", "/admin/v1/devices", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("chưa đăng nhập = %d, muốn 401", rec.Code)
	}

	loginAs := func(code string) *http.Cookie {
		req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"code":"`+code+`","name":"Người thử"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		for _, c := range rec.Result().Cookies() {
			if c.Name == auth.CookieName {
				return c
			}
		}
		t.Fatalf("đăng nhập thất bại: %d %s", rec.Code, rec.Body)
		return nil
	}
	call := func(method, path string, c *http.Cookie) int {
		req := httptest.NewRequest(method, path, nil)
		for k, v := range origin {
			req.Header.Set(k, v)
		}
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	codes := d.Auth.Codes.Snapshot()
	viewer := loginAs(codes.Viewer)
	if got := call("GET", "/admin/v1/devices", viewer); got != http.StatusOK {
		t.Errorf("chỉ xem GET = %d, muốn 200", got)
	}
	if got := call("POST", "/admin/v1/devices/7/approve", viewer); got != http.StatusForbidden {
		t.Errorf("chỉ xem POST = %d, muốn 403", got)
	}
	admin := loginAs(codes.Admin)
	// Qua được kiểm quyền: tới handler, máy 7 không có → 404.
	if got := call("POST", "/admin/v1/devices/7/approve", admin); got != http.StatusNotFound {
		t.Errorf("admin POST = %d, muốn 404", got)
	}
	if rec := do(t, h, "POST", "/admin/v1/devices/7/approve", map[string]string{"Authorization": "Bearer " + testToken}); rec.Code != http.StatusNotFound {
		t.Errorf("Bearer SCS_ADMIN_TOKEN = %d, muốn 404", rec.Code)
	}
	if strings.Contains(logs.String(), testToken) || strings.Contains(logs.String(), codes.Admin) {
		t.Error("log không được chứa token hay mã đăng nhập")
	}
}

func TestBodyLimit(t *testing.T) {
	d, _ := deps(t, nil)
	h := wrap(d, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "quá lớn", http.StatusRequestEntityTooLarge)
		}
	}), httpx.AccessLog(d.Log))
	big := bytes.NewReader(make([]byte, maxRequestBody+1))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", big))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body quá lớn = %d, muốn 413", rec.Code)
	}
}

func TestRecoverPanic(t *testing.T) {
	d, logs := deps(t, nil)
	h := wrap(d, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("hỏng") }), httpx.AccessLog(d.Log))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic = %d, muốn 500", rec.Code)
	}
	if !strings.Contains(logs.String(), "panic trong handler") {
		t.Error("panic phải được ghi log")
	}
}

func TestReadyzReportsFailingCheck(t *testing.T) {
	ttl := httpx.ReadyCacheTTL
	httpx.ReadyCacheTTL = 0
	t.Cleanup(func() { httpx.ReadyCacheTTL = ttl })
	d, logs := deps(t, nil)
	fail := true
	d.ReadyChecks = []httpx.Check{{Name: "postgres", Fn: func(context.Context) error {
		if fail {
			return errors.New("dial tcp 127.0.0.1:5432: connection refused")
		}
		return nil
	}}}
	h := IngestHandler(d)

	for i := 0; i < 3; i++ {
		rec := do(t, h, "GET", "/readyz", nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("DB lỗi: /readyz = %d, muốn 503", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "connection refused") {
			t.Fatal("chi tiết lỗi không được trả ra ngoài")
		}
	}
	// /healthz không phụ thuộc DB.
	if rec := do(t, h, "GET", "/healthz", nil); rec.Code != 200 {
		t.Fatalf("/healthz = %d khi DB lỗi", rec.Code)
	}
	if n := strings.Count(logs.String(), "phụ thuộc chưa sẵn sàng"); n != 1 {
		t.Fatalf("gọi 3 lần chỉ được ghi log 1 lần, được %d", n)
	}

	fail = false
	if rec := do(t, h, "GET", "/readyz", nil); rec.Code != 200 {
		t.Fatalf("DB lên lại: /readyz = %d", rec.Code)
	}
	if !strings.Contains(logs.String(), "phụ thuộc đã sẵn sàng trở lại") {
		t.Fatal("phải ghi log khi phụ thuộc hồi phục")
	}
}

// Mỗi route trên cổng agent có giới hạn body riêng: heartbeat không được gửi tới 2 MB.
func TestIngestPerRouteBodyLimit(t *testing.T) {
	d, _ := deps(t, nil)
	h := IngestHandler(d)
	cases := []struct {
		path string
		size int64
	}{
		{"/v1/enroll", ingestBody.enroll}, {"/v1/heartbeat", ingestBody.heartbeat},
		{"/v1/cert/renew", ingestBody.renew}, {"/v1/logs", ingestBody.logs},
	}
	for _, c := range cases {
		over := httptest.NewRequest("POST", c.path, bytes.NewReader(make([]byte, c.size+1)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, over)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s body %d byte = %d, muốn 413", c.path, c.size+1, rec.Code)
		}
		ok := httptest.NewRequest("POST", c.path, bytes.NewReader(make([]byte, c.size)))
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, ok)
		// heartbeat tự kiểm chứng chỉ trong handler (401 ở đây là đã qua bước kiểm body).
		if rec.Code == http.StatusRequestEntityTooLarge || (rec.Code == http.StatusUnauthorized && c.path != "/v1/heartbeat") {
			t.Errorf("%s body đúng giới hạn = %d, muốn tới handler", c.path, rec.Code)
		}
	}
	// Không có chứng chỉ thì bị chặn trước, không tới bước kiểm body / chiếm chỗ.
	dc, _ := deps(t, func(c *config.Config) { c.TLSClientCA = "/etc/scs/ca.pem" })
	rec := httptest.NewRecorder()
	IngestHandler(dc).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/logs", bytes.NewReader(make([]byte, 1024))))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("không chứng chỉ phải 401 trước khi xét body, được %d", rec.Code)
	}
}

// X-Request-Id client gửi lên không được dùng (chống giả id để làm rối log).
func TestRequestIDNotFromClient(t *testing.T) {
	d, logs := deps(t, nil)
	for name, h := range map[string]http.Handler{"ingest": IngestHandler(d), "admin": AdminHandler(d)} {
		rec := do(t, h, "GET", "/khong-co", map[string]string{"X-Request-Id": "id-gia-mao"})
		if got := rec.Header().Get("X-Request-Id"); got == "id-gia-mao" || len(got) != 16 {
			t.Errorf("%s: X-Request-Id = %q, phải là id server tự sinh", name, got)
		}
	}
	if strings.Contains(logs.String(), "id-gia-mao") {
		t.Error("id giả không được vào log")
	}
}

// Mọi response (JSON, lỗi 401/404/405/413/503) có nosniff + CSP chặn hết; trang giao
// diện giữ CSP riêng (cho script/style từ chính server).
func TestSecurityHeadersEverywhere(t *testing.T) {
	d, _ := deps(t, nil)
	check := func(name string, rec *httptest.ResponseRecorder, wantCSP string) {
		t.Helper()
		h := rec.Header()
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" ||
			!strings.Contains(h.Get("Content-Security-Policy"), wantCSP) {
			t.Errorf("%s (%d) thiếu header bảo mật: %v", name, rec.Code, h)
		}
	}
	ing, adm := IngestHandler(d), AdminHandler(d)
	check("ingest /healthz", do(t, ing, "GET", "/healthz", nil), "default-src 'none'")
	check("ingest 404", do(t, ing, "GET", "/khong-co", nil), "default-src 'none'")
	check("ingest 405", do(t, ing, "GET", "/v1/heartbeat", nil), "default-src 'none'")
	check("admin /auth/me 401", do(t, adm, "GET", "/auth/me", nil), "default-src 'none'")
	check("admin trang chính", do(t, adm, "GET", "/", nil), "script-src 'self'")
	if csp := do(t, adm, "GET", "/", nil).Header().Values("Content-Security-Policy"); len(csp) != 1 {
		t.Errorf("trang chính phải có đúng 1 CSP, có %d", len(csp))
	}
}

// Web quản trị bị gửi dồn dập request đăng nhập sai / bị chặn (429) thì log không tăng theo
// từng request. Trước khi sửa: mỗi response 4xx một dòng.
func TestAdminErrorLogsAggregated(t *testing.T) {
	d, logs := deps(t, nil)
	h := AdminHandler(d)
	for i := 0; i < 500; i++ {
		r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"code":"000001","name":"Do ma"}`))
		r.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	if n := strings.Count(logs.String(), `"msg":"request"`); n > 3 {
		t.Fatalf("500 request lỗi ra %d dòng access log, muốn tối đa 1 dòng mỗi mã", n)
	}
	// Request thành công vẫn ghi mức info như trước.
	do(t, h, "GET", "/", nil)
	if !strings.Contains(logs.String(), `"level":"INFO","msg":"request"`) {
		t.Error("request thành công ở web quản trị phải ghi mức info")
	}
}

// Web quản trị từ chối request đi qua reverse proxy (proxy trên máy server biến mọi
// request thành 127.0.0.1 và vượt qua SCS_ADMIN_ALLOW).
func TestAdminRejectsProxied(t *testing.T) {
	d, logs := deps(t, nil)
	h := AdminHandler(d)
	for _, hdr := range []string{"X-Forwarded-For", "Forwarded", "X-Real-IP", "X-Forwarded-Host"} {
		for _, path := range []string{"/", "/auth/me", "/healthz"} {
			if rec := do(t, h, "GET", path, map[string]string{hdr: "203.0.113.9"}); rec.Code != http.StatusForbidden {
				t.Errorf("%s qua proxy (%s) = %d, muốn 403", path, hdr, rec.Code)
			}
		}
	}
	if n := strings.Count(logs.String(), "reverse proxy"); n != 1 {
		t.Errorf("log chặn proxy phải giới hạn tần suất, được %d dòng", n)
	}
	if rec := do(t, h, "GET", "/", nil); rec.Code != 200 {
		t.Errorf("truy cập trực tiếp = %d", rec.Code)
	}
}

// /readyz gọi dồn dập chỉ hỏi DB một lần trong ReadyCacheTTL.
func TestReadyzCached(t *testing.T) {
	d, _ := deps(t, nil)
	var calls atomic.Int32
	d.ReadyChecks = []httpx.Check{{Name: "postgres", Fn: func(context.Context) error { calls.Add(1); return nil }}}
	h := IngestHandler(d)
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); do(t, h, "GET", "/readyz", nil) }()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("500 request /readyz hỏi DB %d lần, muốn 1", n)
	}
}

// Cổng gộp 6222: khi đã khai CA, request không có chứng chỉ client chỉ vào được
// /v1/enroll và health check; mọi route khác trả 401.
func TestSinglePortClientCertPerRoute(t *testing.T) {
	d, _ := deps(t, func(c *config.Config) { c.TLSClientCA = "/etc/scs/ca.pem" })
	h := IngestHandler(d)

	open := []struct{ method, path string }{
		{"POST", "/v1/enroll"}, {"GET", "/healthz"}, {"GET", "/readyz"},
	}
	for _, r := range open {
		if rec := do(t, h, r.method, r.path, nil); rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s không cần chứng chỉ, nhưng trả 401", r.method, r.path)
		}
	}
	protected := []struct{ method, path string }{
		{"POST", "/v1/heartbeat"}, {"POST", "/v1/logs"}, {"POST", "/v1/cert/renew"},
		{"GET", "/v1/rules/2026.10.27-03"}, {"GET", "/v1/updates/1.0.4"},
	}
	for _, r := range protected {
		if rec := do(t, h, r.method, r.path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s không có chứng chỉ = %d, muốn 401", r.method, r.path, rec.Code)
		}
	}
}

// Giao diện chỉ có ở cổng quản trị, không có ở cổng agent.
func TestWebUIOnlyOnAdmin(t *testing.T) {
	d, _ := deps(t, nil)
	if rec := do(t, AdminHandler(d), "GET", "/", nil); rec.Code != 200 {
		t.Fatalf("admin / = %d, muốn 200", rec.Code)
	}
	if rec := do(t, IngestHandler(d), "GET", "/", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("ingest / = %d, muốn 404", rec.Code)
	}
	// Route quản trị vẫn cần token dù đã có giao diện.
	if rec := do(t, AdminHandler(d), "GET", "/admin/v1/devices", nil); rec.Code == 200 || rec.Code == http.StatusNotImplemented {
		t.Fatalf("/admin/v1/devices không token = %d", rec.Code)
	}
}

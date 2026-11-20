package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/server/internal/auth"
)

// Kho tài khoản và hộp thư giả: đăng nhập thật qua /auth/email/code + /auth/email/login ở từng mức quyền.
type escAccounts struct {
	mu sync.Mutex
	m  map[string]auth.Account
}

func (s *escAccounts) AccountByName(_ context.Context, u string) (auth.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[u]
	if !ok {
		return a, auth.ErrNoAccount
	}
	return a, nil
}
func (s *escAccounts) AccountTaken(context.Context, string, string) (bool, error) { return false, nil }
func (s *escAccounts) CreateAccount(context.Context, auth.Account) error          { return nil }
func (s *escAccounts) ListAccounts(context.Context) ([]auth.Account, error)       { return nil, nil }
func (s *escAccounts) UpdateAccount(_ context.Context, u, st string, l auth.Level, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.m[u]
	a.Status, a.Level = st, l
	s.m[u] = a
	return nil
}
func (s *escAccounts) DeleteAccount(context.Context, string, string) error { return nil }
func (s *escAccounts) TouchLogin(context.Context, string) error            { return nil }
func (s *escAccounts) CountPending(context.Context) (int, error)           { return 0, nil }
func (s *escAccounts) SetSessionLimits(context.Context, string, int, int, string) error {
	return nil
}

type escMail chan string // tiêu đề thư (cuối tiêu đề là mã 6 số)

func (c escMail) Send(_ context.Context, _, subject, _ string) error { c <- subject; return nil }

const escIP, escUA = "10.9.8.7", "trinh-duyet-thu/1.0"

func escCall(h http.Handler, method, path string, c *http.Cookie, ip, ua string) int {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.RemoteAddr = ip + ":40000"
	r.Header.Set("User-Agent", ua)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://example.com")
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// escLogin đăng nhập tài khoản email (mã gửi về hộp thư giả), trả cookie phiên.
func escLogin(t *testing.T, h http.Handler, mail escMail, user string) *http.Cookie {
	t.Helper()
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.RemoteAddr, r.Header["User-Agent"] = escIP+":40000", []string{escUA}
		r.Header.Set("Origin", "http://example.com")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	post("/auth/email/code", `{"username":"`+user+`"}`)
	var subject string
	select {
	case subject = <-mail:
	case <-time.After(3 * time.Second):
		t.Fatalf("không nhận được mã cho %s (giới hạn xin mã?)", user)
	}
	code := subject[len(subject)-6:]
	w := post("/auth/email/login", `{"username":"`+user+`","code":"`+code+`"}`)
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatalf("đăng nhập %s thất bại: %d %s", user, w.Code, w.Body)
	return nil
}

// Leo thang quyền: mỗi mức 1–7 (đăng nhập thật bằng email) gọi MỌI route ghi của web quản trị. Mức
// thấp hơn mức route cần → 403; đủ mức → qua kiểm quyền (không 401/403). Route phiên của quản trị gốc thì
// mọi tài khoản đều 403, kể cả mức 1; route tài khoản / khóa đăng nhập mở cho mức 1 (phạm vi hẹp, xem
// TestDelegatedAccounts).
func TestNoPrivilegeEscalation(t *testing.T) {
	d, _ := deps(t, nil)
	accts := &escAccounts{m: map[string]auth.Account{}}
	mail := make(escMail, 4)
	d.Auth.Accounts, d.Auth.Mail = accts, mail
	h := AdminHandler(d)

	routes := []struct {
		method, path string
		need         auth.Level
	}{
		{"POST", "/admin/v1/accounts/19999", auth.LevelCluster},
		{"GET", "/admin/v1/accounts", auth.LevelCluster},
		{"GET", "/admin/v1/login-blocks", auth.LevelCluster},
		{"GET", "/admin/v1/update", auth.LevelCluster},
		{"POST", "/admin/v1/login-blocks/remove", auth.LevelCluster},
		{"GET", "/admin/v1/sessions", auth.LevelRoot},
		{"POST", "/admin/v1/sessions/abc/revoke", auth.LevelRoot},
		{"POST", "/admin/v1/cluster/approve", auth.LevelCluster},
		{"POST", "/admin/v1/cluster/unapprove", auth.LevelCluster},
		{"POST", "/admin/v1/cluster/reject-join", auth.LevelCluster},
		{"POST", "/admin/v1/cluster/isolate-local", auth.LevelCluster},
		{"POST", "/admin/v1/rules", auth.LevelRules},
		{"POST", "/admin/v1/rules/approve", auth.LevelRules},
		{"POST", "/admin/v1/rules/remove", auth.LevelRules},
		{"POST", "/admin/v1/rules/assign", auth.LevelRules},
		{"POST", "/admin/v1/stores/import", auth.LevelRules},
		{"POST", "/admin/v1/devices/1/approve", auth.LevelAgents},
		{"POST", "/admin/v1/devices/1/reject", auth.LevelAgents},
		{"POST", "/admin/v1/devices/1/revoke", auth.LevelAgents},
		{"POST", "/admin/v1/devices/1/replace", auth.LevelAgents},
		{"POST", "/admin/v1/auto-approve", auth.LevelAgents},
		{"POST", "/admin/v1/agent/rollout", auth.LevelAgents},
		{"POST", "/admin/v1/agent/servers", auth.LevelServers},
		{"POST", "/admin/v1/periods/1/resolve", auth.LevelIncidents},
		{"POST", "/admin/v1/periods/1/handling", auth.LevelIncidents},
		{"POST", "/admin/v1/alerts/1/ack", auth.LevelIncidents},
		{"POST", "/admin/v1/devices/1/test-ring", auth.LevelAgentInfo},
		{"POST", "/admin/v1/agent/config", auth.LevelAgentInfo},
		{"POST", "/admin/v1/agent/labels", auth.LevelAgentInfo},
		{"POST", "/admin/v1/stores/12345", auth.LevelAgentInfo},
		{"GET", "/admin/v1/devices", auth.LevelView},
	}
	for lv := auth.LevelCluster; lv <= auth.LevelView; lv++ {
		user := "1000" + string(rune('0'+lv))
		accts.m[user] = auth.Account{Username: user, Email: user + "@example.com", Status: auth.AccountActive, Level: lv,
			SessionHours: 24, MaxSessions: 20}
		c := escLogin(t, h, mail, user)
		for _, rt := range routes {
			code := escCall(h, rt.method, rt.path, c, escIP, escUA)
			if lv > rt.need && code != http.StatusForbidden {
				t.Errorf("mức %d gọi %s %s (cần mức %d): %d, phải 403", lv, rt.method, rt.path, rt.need, code)
			}
			if lv <= rt.need && (code == http.StatusForbidden || code == http.StatusUnauthorized) {
				t.Errorf("mức %d gọi %s %s (cần mức %d): %d, phải qua kiểm quyền", lv, rt.method, rt.path, rt.need, code)
			}
		}
	}
	// Không đăng nhập: mọi route quản trị 401.
	for _, rt := range routes {
		if code := escCall(h, rt.method, rt.path, nil, escIP, escUA); code != http.StatusUnauthorized {
			t.Errorf("chưa đăng nhập %s %s: %d, phải 401", rt.method, rt.path, code)
		}
	}
}

// Phiên bị đánh cắp: cookie đem sang máy (IP) khác hoặc trình duyệt khác bị từ chối và phiên bị thu hồi
// luôn; dùng lại sau đăng xuất / sau khi khóa tài khoản / sau khi đổi quyền đều không được; CSRF bị chặn.
func TestStolenSessionReplay(t *testing.T) {
	d, logs := deps(t, nil)
	accts := &escAccounts{m: map[string]auth.Account{}}
	mail := make(escMail, 4)
	d.Auth.Accounts, d.Auth.Mail = accts, mail
	h := AdminHandler(d)
	for _, u := range []string{"20001", "20002", "20003", "20004", "20005", "20006"} {
		accts.m[u] = auth.Account{Username: u, Email: u + "@example.com", Status: auth.AccountActive,
			Level: auth.LevelAgents, SessionHours: 24, MaxSessions: 4}
	}
	me := func(c *http.Cookie, ip, ua string) int { return escCall(h, "GET", "/auth/me", c, ip, ua) }

	// 1. Sang IP khác: từ chối, phiên bị thu hồi (người dùng thật cũng phải đăng nhập lại), có log cảnh báo.
	c := escLogin(t, h, mail, "20001")
	if me(c, escIP, escUA) != 200 {
		t.Fatal("phiên vừa đăng nhập phải dùng được")
	}
	if me(c, "10.9.8.99", escUA) != 401 {
		t.Fatal("cookie dùng từ IP khác phải bị từ chối")
	}
	if me(c, escIP, escUA) != 401 {
		t.Fatal("phiên bị dùng từ IP khác phải bị thu hồi luôn")
	}
	if !strings.Contains(logs.String(), "máy / trình duyệt khác") {
		t.Fatal("phải ghi log cảnh báo phiên bị lộ")
	}
	// 2. Cùng IP, trình duyệt khác.
	c = escLogin(t, h, mail, "20002")
	if me(c, escIP, "curl/8.0") != 401 || me(c, escIP, escUA) != 401 {
		t.Fatal("cookie dùng từ trình duyệt khác phải bị từ chối và thu hồi")
	}
	// 3. Dùng lại sau đăng xuất.
	c = escLogin(t, h, mail, "20003")
	if escCall(h, "POST", "/auth/logout", c, escIP, escUA) != 200 || me(c, escIP, escUA) != 401 {
		t.Fatal("đăng xuất xong cookie cũ không còn dùng được")
	}
	// 4. CSRF: đúng cookie nhưng Origin lạ → 403.
	c = escLogin(t, h, mail, "20004")
	r := httptest.NewRequest("POST", "/admin/v1/auto-approve", strings.NewReader(`{"on":true}`))
	r.RemoteAddr, r.Header["User-Agent"] = escIP+":40000", []string{escUA}
	r.Header.Set("Origin", "https://trang-la.example")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("CSRF (Origin lạ) phải 403: %d", w.Code)
	}
	// 5. Khóa tài khoản (quản trị gốc bằng Bearer) → phiên đang mở bị cắt; tài khoản bị khóa không xin mã được.
	root := httptest.NewRequest("POST", "/admin/v1/accounts/20004", strings.NewReader(`{"action":"disable"}`))
	root.Header.Set("Authorization", "Bearer "+testToken)
	root.RemoteAddr = "127.0.0.1:5000"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, root)
	if w.Code != 200 {
		t.Fatalf("khóa tài khoản: %d %s", w.Code, w.Body)
	}
	if me(c, escIP, escUA) != 401 {
		t.Fatal("khóa tài khoản phải cắt phiên đang mở")
	}
	// 6. Bearer sai không rơi xuống cookie (không trộn hai cách xác thực).
	c = escLogin(t, h, mail, "20006")
	r = httptest.NewRequest("GET", "/admin/v1/devices", nil)
	r.RemoteAddr, r.Header["User-Agent"] = escIP+":40000", []string{escUA}
	r.Header.Set("Authorization", "Bearer sai")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("Bearer sai kèm cookie đúng phải 401: %d", w.Code)
	}
	var j map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &j)
}

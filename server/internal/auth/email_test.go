package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// memAccounts: kho tài khoản trong bộ nhớ cho test.
type memAccounts struct {
	mu sync.Mutex
	m  map[string]Account
}

func newMemAccounts() *memAccounts { return &memAccounts{m: map[string]Account{}} }

func (s *memAccounts) AccountByName(_ context.Context, u string) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[u]
	if !ok {
		return a, ErrNoAccount
	}
	return a, nil
}
func (s *memAccounts) AccountTaken(_ context.Context, u, e string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.m {
		if a.Username == u || a.Email == e {
			return true, nil
		}
	}
	return false, nil
}
func (s *memAccounts) CreateAccount(ctx context.Context, a Account) error {
	if t, _ := s.AccountTaken(ctx, a.Username, a.Email); t {
		return ErrAccountExists
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a.Status, a.CreatedAt, a.SessionHours, a.MaxSessions = AccountPending, time.Now(), DefaultSessionHours, DefaultMaxSessions
	s.m[a.Username] = a
	return nil
}
func (s *memAccounts) ListAccounts(context.Context) ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Account
	for _, a := range s.m {
		out = append(out, a)
	}
	return out, nil
}
func (s *memAccounts) UpdateAccount(_ context.Context, u, st string, l Level, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[u]
	if !ok {
		return ErrNoAccount
	}
	a.Status, a.Level, a.ApprovedBy = st, l, by
	s.m[u] = a
	return nil
}
func (s *memAccounts) DeleteAccount(_ context.Context, u, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, u)
	return nil
}
func (s *memAccounts) TouchLogin(context.Context, string) error { return nil }
func (s *memAccounts) SetSessionLimits(_ context.Context, u string, h, n int, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[u]
	if !ok {
		return ErrNoAccount
	}
	a.SessionHours, a.MaxSessions = h, n
	s.m[u] = a
	return nil
}
func (s *memAccounts) CountPending(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.m {
		if a.Status == AccountPending {
			n++
		}
	}
	return n, nil
}

type sentMail struct{ to, subject, body string }

type chanMailer chan sentMail

func (c chanMailer) Send(_ context.Context, to, subject, body string) error {
	c <- sentMail{to, subject, body}
	return nil
}

func (c chanMailer) next(t *testing.T) sentMail {
	t.Helper()
	select {
	case m := <-c:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("không có mail nào được gửi")
	}
	return sentMail{}
}

func (c chanMailer) none(t *testing.T) {
	t.Helper()
	select {
	case m := <-c:
		t.Fatalf("không được gửi mail, nhưng đã gửi: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
}

// codeOf: mã 6 số nằm cuối tiêu đề mail.
func codeOf(m sentMail) string { return m.subject[len(m.subject)-6:] }

var origin = map[string]string{"Origin": "http://example.com"}

func emailSetup(t *testing.T) (*Manager, http.Handler, *memAccounts, chanMailer) {
	t.Helper()
	m, h, _ := newTestManager(t)
	st, mail := newMemAccounts(), make(chanMailer, 10)
	m.Accounts, m.Mail, m.EmailDomains = st, mail, []string{"gmail.com"}
	return m, h, st, mail
}

func post(h http.Handler, path string, v any, cookies ...*http.Cookie) (int, map[string]any, []*http.Cookie) {
	b, _ := json.Marshal(v)
	w := req(h, "POST", path, string(b), origin, cookies...)
	var j map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	return w.Code, j, w.Result().Cookies()
}

func sessionCookie(cs []*http.Cookie) *http.Cookie {
	for _, c := range cs {
		if c.Name == CookieName {
			return c
		}
	}
	return nil
}

func rootCookie(t *testing.T, m *Manager, h http.Handler) *http.Cookie {
	t.Helper()
	_, c := login(t, h, m.Codes.Snapshot().Admin, "Quản trị")
	if c == nil {
		t.Fatal("không đăng nhập được bằng mã quản trị")
	}
	return c
}

func TestEmailRegisterApproveLogin(t *testing.T) {
	m, h, st, mail := emailSetup(t)

	// Đăng ký: chỉ nhận tên miền cho phép.
	if code, _, _ := post(h, "/auth/register", accountReq{Username: "10123", Email: "an@yahoo.com"}); code != 400 {
		t.Fatalf("email ngoài tên miền phải bị từ chối: %d", code)
	}
	if code, _, _ := post(h, "/auth/register", accountReq{Username: "A", Email: "an@gmail.com"}); code != 400 {
		t.Fatalf("tên đăng nhập sai phải bị từ chối: %d", code)
	}
	if code, _, _ := post(h, "/auth/register", accountReq{Username: "10123", Email: "An@Gmail.com"}); code != 200 {
		t.Fatalf("đăng ký: %d", code)
	}
	reg := mail.next(t)
	if reg.to != "an@gmail.com" || !strings.Contains(reg.body, "10123") {
		t.Fatalf("mail xác minh sai: %+v", reg)
	}
	// Mã sai: mã bị hủy luôn, mã đúng nhập sau cũng không còn dùng được (chỉ nhập một lần).
	wrong := "000000"
	if codeOf(reg) == wrong {
		wrong = "111111"
	}
	if code, _, _ := post(h, "/auth/register/verify", accountReq{Username: "10123", Code: wrong}); code != 401 {
		t.Fatalf("mã sai: %d", code)
	}
	if code, _, _ := post(h, "/auth/register/verify", accountReq{Username: "10123", Code: codeOf(reg)}); code != 401 {
		t.Fatalf("mã đã bị hủy sau một lần nhập sai vẫn dùng được: %d", code)
	}
	// Đăng ký lại, nhập đúng → chờ duyệt.
	post(h, "/auth/register", accountReq{Username: "10123", Email: "an@gmail.com"})
	reg = mail.next(t)
	if code, j, _ := post(h, "/auth/register/verify", accountReq{Username: "10123", Code: codeOf(reg)}); code != 200 {
		t.Fatalf("xác minh: %d %v", code, j)
	}
	if a, _ := st.AccountByName(context.Background(), "10123"); a.Status != AccountPending {
		t.Fatalf("tài khoản phải chờ duyệt: %+v", a)
	}
	// Đăng ký trùng tên / email: trả cùng câu, không gửi mail.
	if code, _, _ := post(h, "/auth/register", accountReq{Username: "10456", Email: "an@gmail.com"}); code != 200 {
		t.Fatalf("trùng email phải trả câu chung: %d", code)
	}
	mail.none(t)

	// Chưa duyệt: xin mã đăng nhập không gửi mail (câu trả lời giống hệt).
	_, j1, _ := post(h, "/auth/email/code", accountReq{Username: "10123"})
	_, j2, _ := post(h, "/auth/email/code", accountReq{Username: "khong-co"})
	if j1["message"] != j2["message"] {
		t.Fatalf("câu trả lời phải giống nhau để không dò được tài khoản: %v / %v", j1, j2)
	}
	mail.none(t)

	// Quản trị gốc duyệt với mức 5.
	root := rootCookie(t, m, h)
	if code, _, _ := post(h, "/admin/v1/accounts/10123", map[string]any{"action": "approve", "level": 0}, root); code != 400 {
		t.Fatalf("không được gán mức 0 cho tài khoản: %d", code)
	}
	if code, j, _ := post(h, "/admin/v1/accounts/10123", map[string]any{"action": "approve", "level": 5}, root); code != 200 {
		t.Fatalf("duyệt: %d %v", code, j)
	}
	if n := mail.next(t); !strings.Contains(n.body, LevelNames[LevelIncidents]) {
		t.Fatalf("mail báo duyệt phải ghi quyền: %+v", n)
	}

	// Đăng nhập bằng mã email.
	post(h, "/auth/email/code", accountReq{Username: "10123"})
	lm := mail.next(t)
	code, j, cs := post(h, "/auth/email/login", accountReq{Username: "10123", Code: codeOf(lm)})
	user := sessionCookie(cs)
	if code != 200 || user == nil || j["level"] != float64(5) {
		t.Fatalf("đăng nhập email: %d %v", code, j)
	}
	// Mã đã dùng không dùng lại được.
	if code, _, _ := post(h, "/auth/email/login", accountReq{Username: "10123", Code: codeOf(lm)}); code != 401 {
		t.Fatalf("mã dùng lần hai: %d", code)
	}
	// Mức 5 không vào được trang tài khoản / phiên (chỉ quản trị gốc), không tự nâng quyền được.
	if w := req(h, "GET", "/admin/v1/accounts", "", nil, user); w.Code != 403 {
		t.Fatalf("mức 5 xem tài khoản: %d", w.Code)
	}
	if code, _, _ := post(h, "/admin/v1/accounts/10123", map[string]any{"action": "level", "level": 1}, user); code != 403 {
		t.Fatalf("tự nâng quyền: %d", code)
	}
	if w := req(h, "GET", "/admin/v1/sessions", "", nil, user); w.Code != 403 {
		t.Fatalf("mức 5 xem phiên: %d", w.Code)
	}
	w := req(h, "GET", "/auth/me", "", nil, user)
	if !strings.Contains(w.Body.String(), `"level":5`) || !strings.Contains(w.Body.String(), `"account":"10123"`) {
		t.Fatalf("/auth/me: %s", w.Body)
	}

	// Đổi quyền → phiên cũ bị cắt.
	if code, _, _ := post(h, "/admin/v1/accounts/10123", map[string]any{"action": "level", "level": 7}, root); code != 200 {
		t.Fatal("đổi quyền thất bại")
	}
	if w := req(h, "GET", "/auth/me", "", nil, user); w.Code != 401 {
		t.Fatalf("phiên cũ phải bị cắt sau khi đổi quyền: %d", w.Code)
	}
	// Khóa → không xin được mã nữa.
	post(h, "/admin/v1/accounts/10123", map[string]any{"action": "disable"}, root)
	post(h, "/auth/email/code", accountReq{Username: "10123"})
	mail.none(t)
}

func TestEmailCodeExpires(t *testing.T) {
	m, h, st, mail := emailSetup(t)
	st.m["binh"] = Account{Username: "binh", Email: "binh@gmail.com", Status: AccountActive, Level: LevelView}
	post(h, "/auth/email/code", accountReq{Username: "binh"})
	c := codeOf(mail.next(t))
	m.email.mu.Lock()
	m.email.login["binh"].until = time.Now().Add(-time.Second)
	m.email.mu.Unlock()
	if code, _, _ := post(h, "/auth/email/login", accountReq{Username: "binh", Code: c}); code != 401 {
		t.Fatalf("mã hết hạn vẫn đăng nhập được: %d", code)
	}
}

func TestEmailCodeRateLimit(t *testing.T) {
	_, h, st, mail := emailSetup(t)
	st.m["chi"] = Account{Username: "chi", Email: "chi@gmail.com", Status: AccountActive, Level: LevelView}
	for i := 0; i < codesPerAccount; i++ {
		if code, _, _ := post(h, "/auth/email/code", accountReq{Username: "chi"}); code != 200 {
			t.Fatalf("lần %d: %d", i, code)
		}
		mail.next(t)
	}
	if code, _, _ := post(h, "/auth/email/code", accountReq{Username: "chi"}); code != 429 {
		t.Fatalf("quá số lần xin mã phải bị chặn: %d", code)
	}
	mail.none(t)
}

func TestEmailLoginAttemptLimit(t *testing.T) {
	_, h, _, _ := emailSetup(t)
	for i := 0; i < attemptsPerIP; i++ {
		post(h, "/auth/email/login", accountReq{Username: fmt.Sprintf("u%03d", i), Code: "123456"})
	}
	if code, _, _ := post(h, "/auth/email/login", accountReq{Username: "x01", Code: "123456"}); code != 429 {
		t.Fatalf("dò mã phải bị chặn theo IP: %d", code)
	}
}

func TestEmailDisabledWithoutMailer(t *testing.T) {
	_, h, _ := newTestManager(t)
	if code, _, _ := post(h, "/auth/email/code", accountReq{Username: "an"}); code != 503 {
		t.Fatalf("chưa cấu hình mail: %d", code)
	}
	w := req(h, "GET", "/auth/options", "", nil)
	if !strings.Contains(w.Body.String(), `"email":false`) {
		t.Fatalf("/auth/options: %s", w.Body)
	}
}

func TestLevels(t *testing.T) {
	m, h, _ := newTestManager(t)
	mux := http.NewServeMux()
	m.Mount(mux)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	mux.Handle("POST /need/3", m.RequireLevel(LevelAgents)(ok))
	_ = h
	for lv, want := range map[Level]int{LevelRoot: 200, LevelCluster: 200, LevelAgents: 200, LevelServers: 403, LevelView: 403} {
		cookie, _, _ := m.sess.createLevel("u", "u", lv, "100.64.0.10", "", time.Hour, 4)
		if w := req(mux, "POST", "/need/3", "{}", origin, &http.Cookie{Name: CookieName, Value: cookie}); w.Code != want {
			t.Errorf("mức %d làm việc cần mức 3: %d, muốn %d", lv, w.Code, want)
		}
	}
	// Mã Chỉ xem (SSH) = mức 7, mã quản trị = mức 0.
	_, v := login(t, mux, m.Codes.Snapshot().Viewer, "Xem")
	if w := req(mux, "POST", "/need/3", "{}", origin, v); w.Code != 403 {
		t.Fatalf("mã chỉ xem: %d", w.Code)
	}
}

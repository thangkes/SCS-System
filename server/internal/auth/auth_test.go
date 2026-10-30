package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock là đồng hồ giả cho test.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock               { return &clock{t: time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)} }
func seqGen(vals ...string) func() string {
	i := 0
	return func() string { v := vals[i%len(vals)]; i++; return v }
}

// liveCodes trả mọi mã đang còn hiệu lực (hiện tại + đang ân hạn).
func liveCodes(c *Codes) []string {
	now := c.now()
	var out []string
	for _, m := range []map[Role]code{c.cur, c.prev} {
		for _, x := range m {
			if x.value != "" && now.Before(x.until) {
				out = append(out, x.value)
			}
		}
	}
	return out
}

func TestCodesBasic(t *testing.T) {
	ck := newClock()
	c := newCodes(ck.now, randomCode)
	s := c.Snapshot()
	if len(s.Admin) != 6 || len(s.Viewer) != 6 || s.Admin == s.Viewer {
		t.Fatalf("mã sai: %+v", s)
	}
	if !s.Until.Equal(ck.t.Add(time.Minute)) {
		t.Fatalf("phải đổi vào đầu phút sau, được %v", s.Until)
	}

	// Dùng mã Admin: đúng quyền, mã Admin đổi, mã Chỉ xem giữ nguyên, mã cũ không dùng lại được.
	if r, ok := c.Use(s.Admin); !ok || r != RoleAdmin {
		t.Fatalf("mã admin: %v %v", r, ok)
	}
	s2 := c.Snapshot()
	if s2.Admin == s.Admin || s2.Viewer != s.Viewer {
		t.Fatalf("chỉ mã Admin được đổi: trước %+v sau %+v", s, s2)
	}
	if _, ok := c.Use(s.Admin); ok {
		t.Fatal("mã đã dùng không được dùng lại")
	}
	if r, ok := c.Use(s2.Viewer); !ok || r != RoleViewer {
		t.Fatalf("mã chỉ xem: %v %v", r, ok)
	}
}

func TestCodesGraceAndRotation(t *testing.T) {
	ck := newClock()
	c := newCodes(ck.now, randomCode)
	old := c.Snapshot()

	ck.add(time.Minute + 5*time.Second) // sang phút mới, còn trong 10 giây ân hạn
	cur := c.Snapshot()
	if cur.Admin == old.Admin {
		t.Fatal("sang phút mới phải đổi mã")
	}
	if r, ok := c.Use(old.Viewer); !ok || r != RoleViewer {
		t.Fatal("mã cũ còn dùng được trong 10 giây ân hạn")
	}

	ck.add(6 * time.Second) // quá ân hạn
	if _, ok := c.Use(old.Admin); ok {
		t.Fatal("quá 10 giây ân hạn thì mã cũ hết hiệu lực")
	}
}

// Đồng hồ hệ thống bị lùi 1 giờ: mã phải đổi ngay, mã cũ (kể cả mã đang ân hạn) hết hiệu lực,
// và sau đó vẫn đổi đều mỗi phút. Trước khi sửa: mã giữ nguyên suốt 1 giờ.
func TestCodesClockJumpBack(t *testing.T) {
	ck := newClock()
	c := newCodes(ck.now, randomCode)
	ck.add(time.Minute + 2*time.Second) // có mã cũ đang ân hạn
	before := c.Snapshot()
	prev := c.prev[RoleAdmin].value

	ck.add(-time.Hour)
	after := c.Snapshot()
	if after.Admin == before.Admin || after.Viewer == before.Viewer {
		t.Fatal("đồng hồ lùi thì phải đổi mã ngay")
	}
	if !after.Until.After(ck.now()) || after.Until.Sub(ck.now()) > time.Minute {
		t.Fatalf("hạn mã mới phải trong vòng 1 phút tới, đang %v", after.Until.Sub(ck.now()))
	}
	for _, old := range []string{before.Admin, before.Viewer, prev} {
		if _, ok := c.Use(old); ok {
			t.Fatalf("mã trước khi lùi giờ (%s) phải hết hiệu lực", old)
		}
	}
	ck.add(time.Minute)
	if c.Snapshot().Admin == after.Admin {
		t.Fatal("sau khi lùi giờ vẫn phải đổi mã mỗi phút")
	}
	if a := c.Snapshot(); a.Admin == a.Viewer {
		t.Fatal("hai mã không được trùng")
	}
}

// Phiên hết hạn mà không ai mở lại / xem danh sách vẫn bị dọn khi có đăng nhập mới.
func TestSessionsSweptOnCreate(t *testing.T) {
	ck := newClock()
	s := newSessions(ck.now)
	for i := 0; i < 50; i++ {
		s.create("Người dùng", RoleViewer, "10.0.0.1", "test")
	}
	ck.add(sessionMaxAge + time.Minute)
	s.create("Người mới", RoleAdmin, "10.0.0.2", "test")
	if n := len(s.m); n != 1 {
		t.Fatalf("còn %d phiên trong RAM, muốn 1 (50 phiên hết hạn phải bị dọn)", n)
	}
}

func TestCodesWrongRotatesEarly(t *testing.T) {
	ck := newClock()
	c := newCodes(ck.now, randomCode)
	early := 0
	c.OnRotateEarly = func() { early++ }
	before := c.Snapshot()
	for i := 0; i < wrongToRotate; i++ {
		c.Use("not-a-code")
	}
	after := c.Snapshot()
	if early != 1 || after.Admin == before.Admin || after.Viewer == before.Viewer {
		t.Fatalf("sai %d lần phải đổi cả hai mã: early=%d %+v → %+v", wrongToRotate, early, before, after)
	}
	if _, ok := c.Use(before.Admin); ok {
		t.Fatal("đổi sớm thì mã cũ không còn ân hạn")
	}
}

// Kiểm chứng bất biến "không bao giờ có hai mã còn hiệu lực trùng nhau" qua
// 3.000 bước ngẫu nhiên, với bộ sinh mã bị ép chỉ có 6 giá trị (rất dễ trùng).
func TestCodesNeverCollide(t *testing.T) {
	ck := newClock()
	rng := rand.New(rand.NewSource(1))
	pool := []string{"111111", "222222", "333333", "444444", "555555", "666666"}
	c := newCodes(ck.now, func() string { return pool[rng.Intn(len(pool))] })

	for step := 0; step < 3000; step++ {
		switch rng.Intn(5) {
		case 0:
			ck.add(time.Duration(rng.Intn(20)) * time.Second)
		case 1:
			s := c.Snapshot()
			c.Use([]string{s.Admin, s.Viewer}[rng.Intn(2)])
		case 2:
			c.Use(pool[rng.Intn(len(pool))]) // có thể trúng mã cũ đang ân hạn
		case 3:
			c.Use("000000")
		case 4:
			c.Snapshot()
		}
		c.mu.Lock()
		live := liveCodes(c)
		c.mu.Unlock()
		seen := map[string]bool{}
		for _, v := range live {
			if seen[v] {
				t.Fatalf("bước %d: mã %s trùng giữa các mã còn hiệu lực %v", step, v, live)
			}
			seen[v] = true
		}
	}
}

// Test đột biến: nếu bỏ kiểm tra trùng thì test trên phải bắt được lỗi.
func TestCollisionCheckIsNeeded(t *testing.T) {
	ck := newClock()
	c := newCodes(ck.now, seqGen("123456", "123456", "654321"))
	s := c.Snapshot()
	if s.Admin == s.Viewer {
		t.Fatal("bộ sinh trả trùng thì phải sinh lại")
	}
}

func TestLimiter(t *testing.T) {
	ck := newClock()
	l := newLimiter(ck.now)
	for i := 0; i < ipMaxFails; i++ {
		if a := l.begin("1.1.1.1"); !a.ok || a.left != ipMaxFails-1-i {
			t.Fatalf("lần %d: %+v", i, a)
		}
	}
	if a := l.begin("1.1.1.1"); a.ok || a.wait <= 0 {
		t.Fatal("sai 10 lần thì IP bị chặn")
	}
	if a := l.begin("2.2.2.2"); !a.ok {
		t.Fatal("IP khác không bị ảnh hưởng")
	}
	ck.add(ipWindow)
	if a := l.begin("1.1.1.1"); !a.ok {
		t.Fatal("hết 15 phút thì được thử lại")
	}

	// Đăng nhập đúng thì lượt được trả lại.
	l3 := newLimiter(ck.now)
	for i := 0; i < 3*ipMaxFails; i++ {
		if a := l3.begin("3.3.3.3"); !a.ok {
			t.Fatalf("lượt đúng mã không được tính là sai (lần %d)", i)
		}
		l3.refund("3.3.3.3")
	}

	// Toàn hệ thống: 20 lần sai trong 5 phút từ nhiều IP → khóa mọi IP 5 phút.
	l2 := newLimiter(ck.now)
	locked := false
	for i := 0; i < globalMaxFails; i++ {
		locked = l2.begin("10.0.0." + string(rune('a'+i))).locked
	}
	if !locked {
		t.Fatal("20 lần sai toàn hệ thống phải khóa")
	}
	if a := l2.begin("9.9.9.9"); a.ok {
		t.Fatal("đang khóa toàn hệ thống thì IP mới cũng bị chặn")
	}
	ck.add(globalLockFor)
	if a := l2.begin("9.9.9.9"); !a.ok {
		t.Fatal("hết 5 phút khóa thì mở lại")
	}
}

// Bị khóa liên tiếp thì thời gian khóa gấp đôi (tối đa 60 phút); yên 1 giờ thì về 5 phút.
func TestLimiterEscalates(t *testing.T) {
	ck := newClock()
	l := newLimiter(ck.now)
	lockOnce := func() time.Duration {
		var a attempt
		for i := 0; i < globalMaxFails; i++ {
			a = l.begin("10.0.0." + string(rune('a'+i%26)) + string(rune('a'+i/26)))
		}
		if !a.locked {
			t.Fatal("20 lần sai phải khóa")
		}
		ck.add(a.lockFor) // chờ hết khóa
		return a.lockFor
	}
	for _, want := range []time.Duration{5, 10, 20, 40, 60, 60} {
		ck.add(16 * time.Minute) // qua cửa sổ đếm theo IP
		if got := lockOnce(); got != want*time.Minute {
			t.Fatalf("khóa %v, muốn %v phút", got, want)
		}
	}
	ck.add(lockQuiet)
	if got := lockOnce(); got != globalLockFor {
		t.Fatalf("yên 1 giờ thì về %v, được %v", globalLockFor, got)
	}
}

// Gửi 200 request cùng lúc, giữ body lại để mọi request đều đã vào handler trước
// khi lần sai đầu tiên được ghi. Trước khi sửa: cả 200 request đều được so mã.
func TestLimiterBurstNotBypassed(t *testing.T) {
	m := NewManager("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	m.Mount(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	const n = 200
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		status = map[int]int{}
		pws    = make([]*io.PipeWriter, n)
	)
	for i := range pws {
		pr, pw := io.Pipe()
		pws[i] = pw
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := http.NewRequest("POST", ts.URL+"/auth/login", pr)
			r.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				return
			}
			resp.Body.Close()
			mu.Lock()
			status[resp.StatusCode]++
			mu.Unlock()
		}()
	}
	time.Sleep(time.Second) // header đã tới, các handler đang chờ body
	for _, pw := range pws {
		pw.Write([]byte(`{"code":"000001","name":"Dò mã"}`))
		pw.Close()
	}
	wg.Wait()
	if status[http.StatusUnauthorized] > ipMaxFails {
		t.Fatalf("chỉ được so mã tối đa %d lần, thực tế %d (%v)", ipMaxFails, status[http.StatusUnauthorized], status)
	}
}

func TestSessions(t *testing.T) {
	ck := newClock()
	s := newSessions(ck.now)
	cookie, sess := s.create("An", RoleAdmin, "1.1.1.1", "ua")
	if strings.Contains(sess.ID, cookie) || len(cookie) < 40 {
		t.Fatal("ID công khai không được là cookie")
	}
	if _, ok := s.get(cookie, "1.1.1.1"); !ok {
		t.Fatal("phiên vừa tạo phải dùng được")
	}
	if _, ok := s.get(sess.ID, "1.1.1.1"); ok {
		t.Fatal("dùng ID công khai làm cookie không được")
	}
	ck.add(sessionMaxAge)
	if _, ok := s.get(cookie, "1.1.1.1"); ok {
		t.Fatal("quá 24 giờ phải hết phiên")
	}
	cookie2, sess2 := s.create("B", RoleViewer, "", "")
	if _, ok := s.revoke(sess2.ID); !ok {
		t.Fatal("thu hồi theo ID")
	}
	if _, ok := s.get(cookie2, ""); ok {
		t.Fatal("phiên đã thu hồi không dùng được")
	}
}

// ---------------------------------------------------------------- HTTP

func newTestManager(t *testing.T) (*Manager, http.Handler, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	m := NewManager("api-token-0123456789abcdef0123456789", slog.New(slog.NewTextHandler(&logs, nil)))
	mux := http.NewServeMux()
	m.Mount(mux)
	mux.Handle("GET /admin/v1/thing", m.Require(RoleViewer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("read")) })))
	mux.Handle("POST /admin/v1/thing", m.Require(RoleAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("write")) })))
	return m, mux, &logs
}

func req(h http.Handler, method, path, body string, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = "100.64.0.10:5555"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func login(t *testing.T, h http.Handler, code, name string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	b, _ := json.Marshal(loginReq{Code: code, Name: name})
	w := req(h, "POST", "/auth/login", string(b), map[string]string{"Origin": "http://example.com"})
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName {
			return w, c
		}
	}
	return w, nil
}

func TestLoginFlowAndRoles(t *testing.T) {
	m, h, logs := newTestManager(t)
	codes := m.Codes.Snapshot()

	// Mã Chỉ xem: đọc được, ghi bị 403.
	w, viewer := login(t, h, codes.Viewer[:3]+" "+codes.Viewer[3:], "Người xem")
	if w.Code != 200 || viewer == nil {
		t.Fatalf("đăng nhập chỉ xem: %d %s", w.Code, w.Body)
	}
	if !viewer.HttpOnly || viewer.SameSite != http.SameSiteStrictMode || viewer.MaxAge != 0 {
		t.Fatalf("cookie phải HttpOnly, SameSite=Strict, không MaxAge: %+v", viewer)
	}
	if w := req(h, "GET", "/admin/v1/thing", "", nil, viewer); w.Code != 200 {
		t.Fatalf("chỉ xem đọc được: %d", w.Code)
	}
	if w := req(h, "POST", "/admin/v1/thing", "", map[string]string{"Origin": "http://example.com"}, viewer); w.Code != http.StatusForbidden {
		t.Fatalf("chỉ xem ghi phải 403, được %d", w.Code)
	}

	// Mã Admin: ghi được.
	_, admin := login(t, h, codes.Admin, "Quản trị")
	if admin == nil {
		t.Fatal("đăng nhập admin thất bại")
	}
	if w := req(h, "POST", "/admin/v1/thing", "", map[string]string{"Origin": "http://example.com"}, admin); w.Code != 200 {
		t.Fatalf("admin ghi được: %d", w.Code)
	}
	if w := req(h, "GET", "/auth/me", "", nil, admin); !strings.Contains(w.Body.String(), `"role":"admin"`) {
		t.Fatalf("/auth/me: %s", w.Body)
	}

	// CSRF: POST từ trang khác bị chặn dù có cookie.
	if w := req(h, "POST", "/admin/v1/thing", "", map[string]string{"Origin": "https://evil.example"}, admin); w.Code != http.StatusForbidden {
		t.Fatalf("POST khác origin phải 403, được %d", w.Code)
	}

	// Admin xem và thu hồi phiên của người xem.
	w = req(h, "GET", "/admin/v1/sessions", "", nil, admin)
	var list struct{ Sessions []Session }
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Sessions) != 2 {
		t.Fatalf("phải có 2 phiên, được %d", len(list.Sessions))
	}
	var viewerID string
	for _, s := range list.Sessions {
		if s.Role == RoleViewer {
			viewerID = s.ID
		}
	}
	if w := req(h, "POST", "/admin/v1/sessions/"+viewerID+"/revoke", "", map[string]string{"Origin": "http://example.com"}, admin); w.Code != 200 {
		t.Fatalf("thu hồi: %d", w.Code)
	}
	if w := req(h, "GET", "/admin/v1/thing", "", nil, viewer); w.Code != http.StatusUnauthorized {
		t.Fatalf("phiên đã thu hồi phải 401, được %d", w.Code)
	}

	// Đăng xuất.
	req(h, "POST", "/auth/logout", "", map[string]string{"Origin": "http://example.com"}, admin)
	if w := req(h, "GET", "/auth/me", "", nil, admin); w.Code != http.StatusUnauthorized {
		t.Fatalf("đăng xuất xong phải 401, được %d", w.Code)
	}

	if strings.Contains(logs.String(), admin.Value) || strings.Contains(logs.String(), codes.Admin) {
		t.Fatal("log không được chứa cookie hoặc mã đăng nhập")
	}
}

func TestLoginRejects(t *testing.T) {
	m, h, _ := newTestManager(t)
	codes := m.Codes.Snapshot()

	if w, _ := login(t, h, "000000", "Ai đó"); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"left":9`) {
		t.Fatalf("sai mã: %d %s", w.Code, w.Body)
	}
	if w, _ := login(t, h, codes.Admin, "x"); w.Code != http.StatusBadRequest {
		t.Fatalf("tên quá ngắn phải 400, được %d", w.Code)
	}
	// Từ trang khác (CSRF) hoặc không phải JSON: từ chối, không tiêu mã.
	b, _ := json.Marshal(loginReq{Code: codes.Admin, Name: "Kẻ xấu"})
	if w := req(h, "POST", "/auth/login", string(b), map[string]string{"Origin": "https://evil.example"}); w.Code != http.StatusForbidden {
		t.Fatalf("login khác origin phải 403, được %d", w.Code)
	}
	if w := req(h, "POST", "/auth/login", string(b), map[string]string{"Content-Type": "text/plain"}); w.Code != http.StatusForbidden {
		t.Fatalf("login không phải JSON phải 403, được %d", w.Code)
	}
	if w, c := login(t, h, codes.Admin, "Quản trị"); w.Code != 200 || c == nil {
		t.Fatalf("mã vẫn còn dùng được sau các lần bị chặn: %d", w.Code)
	}

	// Sai 10 lần từ một IP → 429.
	m2, h2, _ := newTestManager(t)
	_ = m2
	for i := 0; i < ipMaxFails; i++ {
		login(t, h2, "000000", "Dò mã")
	}
	if w, _ := login(t, h2, "000000", "Dò mã"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("quá 10 lần sai phải 429, được %d", w.Code)
	}
}

// Tên chứa ký tự vô hình / đảo chiều (giả mạo tên hiển thị) bị từ chối; tên tiếng Việt vẫn được.
func TestCleanNameRejectsInvisible(t *testing.T) {
	for _, bad := range []string{
		"\u202enimdA gnahT", // đảo chiều: hiện thành "Thang Admin"
		"Th\u200bang",       // khoảng trắng độ rộng 0
		"\u2066Admin\u2069", // cô lập hướng chữ
		"Ad\ufeffmin",       // BOM giữa chuỗi
		"Ad\ue000min",       // vùng ký tự riêng
		"Ad\ufffdmin",       // UTF-8 hỏng
		"Ad\x00min", "a",
	} {
		if got, ok := cleanName(bad); ok {
			t.Errorf("cleanName(%q) phải bị từ chối, lại nhận %q", bad, got)
		}
	}
	for in, want := range map[string]string{
		"Nguyễn Văn An": "Nguyễn Văn An",
		"Nguye\u0302\u0303n  Va\u0306n   Tha\u0306\u0301ng": "Nguye\u0302\u0303n Va\u0306n Tha\u0306\u0301ng", // dấu tổ hợp
		"  Kỹ thuật\tviên  ":                                "Kỹ thuật viên",
	} {
		if got, ok := cleanName(in); !ok || got != want {
			t.Errorf("cleanName(%q) = %q, %v; muốn %q", in, got, ok, want)
		}
	}
}

func TestBearerToken(t *testing.T) {
	_, h, _ := newTestManager(t)
	if w := req(h, "POST", "/admin/v1/thing", "", map[string]string{"Authorization": "Bearer api-token-0123456789abcdef0123456789"}); w.Code != 200 {
		t.Fatalf("Bearer đúng phải là Admin: %d", w.Code)
	}
	if w := req(h, "GET", "/admin/v1/thing", "", map[string]string{"Authorization": "Bearer sai"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("Bearer sai phải 401: %d", w.Code)
	}
	if w := req(h, "GET", "/admin/v1/thing", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("không đăng nhập phải 401: %d", w.Code)
	}
}

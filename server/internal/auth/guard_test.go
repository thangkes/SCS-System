package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// postFrom: POST JSON từ một IP, kèm cookie.
func postFrom(h http.Handler, path string, v any, ip string, cookies ...*http.Cookie) (int, string) {
	b, _ := json.Marshal(v)
	r := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
	r.RemoteAddr = ip + ":5555"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://example.com")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func devCookie(id string) *http.Cookie { return &http.Cookie{Name: DeviceCookie, Value: id} }

// memBlocks: kho khóa trong bộ nhớ.
type memBlocks struct {
	mu sync.Mutex
	m  map[string]Block
}

func (s *memBlocks) LoadBlocks(context.Context) ([]Block, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Block
	for _, b := range s.m {
		out = append(out, b)
	}
	return out, nil
}
func (s *memBlocks) SaveBlock(_ context.Context, b Block) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[b.Kind+"|"+b.Key] = b
	return nil
}
func (s *memBlocks) DeleteBlocks(_ context.Context, kind, key, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, b := range s.m {
		if kind == "" || (b.Kind == kind && b.Key == key) {
			delete(s.m, id)
		}
	}
	return nil
}

// Nhập sai mã email 5 lần liên tiếp cho cùng tài khoản → IP bị khóa 24 giờ (cả xin mã, đăng ký); IP khác
// vẫn dùng được; khóa lưu kho và nạp lại được sau khởi động lại; gỡ khóa thì dùng lại được.
func TestBlockAfterFiveWrongCodes(t *testing.T) {
	m, h, st, mail := emailSetup(t)
	blocks := &memBlocks{m: map[string]Block{}}
	m.Blocks = blocks
	st.m["10123"] = Account{Username: "10123", Email: "a@gmail.com", Status: AccountActive, Level: LevelView,
		SessionHours: 24, MaxSessions: 4}
	const ip = "203.0.113.5"
	for i := 0; i < codeFailMax; i++ {
		if code, _ := postFrom(h, "/auth/email/login", accountReq{Username: "10123", Code: "000000"}, ip); code != 401 {
			t.Fatalf("lần sai %d: %d", i+1, code)
		}
	}
	if code, body := postFrom(h, "/auth/email/code", accountReq{Username: "10123"}, ip); code != 403 || !strings.Contains(body, "IP này bị khóa") {
		t.Fatalf("sai 5 lần phải khóa IP: %d %s", code, body)
	}
	if code, _ := postFrom(h, "/auth/register", accountReq{Username: "10999", Email: "x@gmail.com"}, ip); code != 403 {
		t.Fatalf("IP bị khóa thì đăng ký cũng bị chặn: %d", code)
	}
	mail.none(t)
	// IP khác: vẫn xin mã, đăng nhập được.
	if code, _ := postFrom(h, "/auth/email/code", accountReq{Username: "10123"}, "203.0.113.6"); code != 200 {
		t.Fatalf("IP khác: %d", code)
	}
	c := codeOf(mail.next(t))
	if code, _ := postFrom(h, "/auth/email/login", accountReq{Username: "10123", Code: c}, "203.0.113.6"); code != 200 {
		t.Fatalf("IP khác đăng nhập: %d", code)
	}
	// Khóa đã lưu kho, hạn 24 giờ.
	if b, ok := blocks.m[BlockIP+"|"+ip]; !ok || b.Until.Sub(b.Created) != codeFailBlock {
		t.Fatalf("khóa phải lưu kho với hạn 24 giờ: %+v", blocks.m)
	}
	// Khởi động lại: nạp từ kho, vẫn bị khóa.
	m2, h2, _, _ := emailSetup(t)
	m2.Blocks = blocks
	if err := m2.LoadBlocks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code, _ := postFrom(h2, "/auth/email/code", accountReq{Username: "10123"}, ip); code != 403 {
		t.Fatalf("khởi động lại vẫn phải khóa: %d", code)
	}
	// Gỡ khóa.
	if n, err := m2.Unblock(context.Background(), BlockIP, ip, "Quản trị"); err != nil || n != 1 || len(blocks.m) != 0 {
		t.Fatalf("gỡ khóa: %d %v %+v", n, err, blocks.m)
	}
	if code, _ := postFrom(h2, "/auth/email/code", accountReq{Username: "10123"}, ip); code != 200 {
		t.Fatalf("gỡ khóa xong phải dùng lại được: %d", code)
	}
}

// Nhập đúng mã thì đếm lại: 4 sai + 1 đúng + 4 sai không bị khóa. Sai cho tài khoản khác nhau đếm riêng.
func TestCorrectCodeResetsCount(t *testing.T) {
	m, _, _, _ := emailSetup(t)
	s := subject{kind: BlockIP, key: "203.0.113.7", ip: "203.0.113.7"}
	for i := 0; i < codeFailMax-1; i++ {
		m.codeFailed(s, "10123")
		m.codeFailed(s, "10456")
	}
	m.codeOK(s, "10123")
	for i := 0; i < codeFailMax-1; i++ {
		m.codeFailed(s, "10123")
	}
	if len(m.ListBlocks()) != 0 {
		t.Fatalf("chưa đủ 5 lần liên tiếp cho cùng tài khoản thì chưa khóa: %+v", m.ListBlocks())
	}
	m.codeFailed(s, "10456")
	if b := m.ListBlocks(); len(b) != 1 || !strings.Contains(b[0].Reason, "10456") {
		t.Fatalf("lần sai thứ 5 cho 10456 phải khóa: %+v", b)
	}
}

// Mã SSH của quản trị gốc: sai 5 lần → khóa IP; đúng giữa chừng thì đếm lại.
func TestRootCodeBlock(t *testing.T) {
	m, h, _ := newTestManager(t)
	for i := 0; i < codeFailMax-1; i++ {
		login(t, h, "000000", "Dò mã")
	}
	if w, _ := login(t, h, m.Codes.Snapshot().Admin, "Quản trị"); w.Code != 200 {
		t.Fatalf("mã đúng ở lần 5: %d", w.Code)
	}
	for i := 0; i < codeFailMax-1; i++ {
		login(t, h, "000000", "Dò mã")
	}
	if w, _ := login(t, h, m.Codes.Snapshot().Admin, "Quản trị"); w.Code != 200 {
		t.Fatalf("đúng giữa chừng phải đếm lại: %d %s", w.Code, w.Body)
	}
}

// Sai tên / đăng ký sai định dạng quá 10 lần trong 24 giờ → khóa 7 ngày. Lần thứ 10 chưa khóa; quá 24 giờ thì
// các lần cũ không tính.
func TestBlockAfterTenUnknownUsers(t *testing.T) {
	m, h, _, _ := emailSetup(t)
	clk := newClock()
	m.guard.now = clk.now
	const ip = "198.51.100.9"
	for i := 0; i < userMissMax; i++ {
		if code, _ := postFrom(h, "/auth/email/code", accountReq{Username: "!!"}, ip); code != 200 {
			t.Fatalf("tên sai định dạng vẫn trả câu chung: %d", code)
		}
	}
	if len(m.ListBlocks()) != 0 {
		t.Fatal("10 lần chưa được khóa")
	}
	clk.add(userMissWindow) // các lần cũ hết hạn
	for i := 0; i < userMissMax; i++ {
		postFrom(h, "/auth/register", accountReq{Username: "abc", Email: "x@gmail.com"}, ip)
	}
	if len(m.ListBlocks()) != 0 {
		t.Fatal("lần cũ quá 24 giờ không được tính")
	}
	postFrom(h, "/auth/email/code", accountReq{Username: "khong-co"}, ip) // tên không có: lần thứ 11
	b := m.ListBlocks()
	if len(b) != 1 || b[0].Kind != BlockIP || b[0].Until.Sub(b[0].Created) != userMissBlock {
		t.Fatalf("lần thứ 11 phải khóa IP 7 ngày: %+v", b)
	}
	if code, _ := postFrom(h, "/auth/email/code", accountReq{Username: "10123"}, ip); code != 403 {
		t.Fatalf("đang khóa: %d", code)
	}
	clk.add(userMissBlock)
	if code, _ := postFrom(h, "/auth/email/code", accountReq{Username: "10123"}, ip); code != 200 {
		t.Fatalf("hết 7 ngày phải tự mở: %d", code)
	}
}

// IP siêu thị: khóa theo mã thiết bị (cookie), người khác cùng siêu thị vẫn đăng nhập được; quá 3 thiết
// bị bị khóa trong 24 giờ thì khóa cả IP; không có cookie thì khóa nhóm "không cookie" của IP đó.
func TestSiteIPBlocksByDevice(t *testing.T) {
	m, h, _, _ := emailSetup(t)
	const store = "192.0.2.50"
	asked := 0
	m.SiteIP = func(_ context.Context, ip string) (bool, error) { asked++; return ip == store, nil }
	fail := func(dev string) {
		var cs []*http.Cookie
		if dev != "" {
			cs = append(cs, devCookie(dev))
		}
		for i := 0; i < codeFailMax; i++ {
			postFrom(h, "/auth/email/login", accountReq{Username: "10123", Code: "000000"}, store, cs...)
		}
	}
	devs := []string{strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)}
	fail(devs[0])
	if code, body := postFrom(h, "/auth/email/code", accountReq{Username: "10123"}, store, devCookie(devs[0])); code != 403 || !strings.Contains(body, "Thiết bị này") {
		t.Fatalf("thiết bị A phải bị khóa: %d %s", code, body)
	}
	if code, _ := postFrom(h, "/auth/email/code", accountReq{Username: "10123"}, store, devCookie(devs[1])); code != 200 {
		t.Fatalf("thiết bị B cùng siêu thị vẫn dùng được: %d", code)
	}
	fail("") // không cookie
	if code, _ := postFrom(h, "/auth/email/code", accountReq{Username: "10123"}, store); code != 403 {
		t.Fatalf("không cookie phải bị khóa: %d", code)
	}
	if code, _ := postFrom(h, "/auth/email/code", accountReq{Username: "10123"}, store, devCookie(devs[2])); code != 200 {
		t.Fatalf("2 thiết bị bị khóa: IP chưa bị khóa: %d", code)
	}
	fail(devs[1])
	if len(m.ListBlocks()) != 3 {
		t.Fatalf("3 thiết bị bị khóa, chưa khóa IP: %+v", m.ListBlocks())
	}
	fail(devs[2]) // thiết bị thứ 4 → quá trần
	if code, body := postFrom(h, "/auth/email/code", accountReq{Username: "10123"}, store, devCookie(devs[3])); code != 403 || !strings.Contains(body, "IP này") {
		t.Fatalf("quá 3 thiết bị bị khóa phải khóa cả IP siêu thị: %d %s", code, body)
	}
	if asked > 2 {
		t.Fatalf("IP siêu thị phải được nhớ 5 phút, đã hỏi DB %d lần", asked)
	}
	// Mở trang đăng nhập: được cấp mã thiết bị.
	w := req(h, "GET", "/auth/options", "", nil)
	var dev *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == DeviceCookie {
			dev = c
		}
	}
	if dev == nil || len(dev.Value) != 32 || !dev.HttpOnly || dev.MaxAge < 300*24*3600 {
		t.Fatalf("phải cấp cookie mã thiết bị HttpOnly dài hạn: %+v", dev)
	}
}

// Mức 1 quản lý tài khoản trong phạm vi mức 5–7; mọi vượt phạm vi bị 403 và không đổi dữ liệu.
func TestDelegatedAccounts(t *testing.T) {
	m, h, st, mail := emailSetup(t)
	add := func(u, status string, lv Level) {
		st.m[u] = Account{Username: u, Email: u + "@gmail.com", Status: status, Level: lv, SessionHours: 24, MaxSessions: 4}
	}
	add("11111", AccountActive, LevelCluster) // người làm (mức 1)
	add("22222", AccountActive, LevelRules)
	add("33333", AccountActive, LevelAgents)
	add("55555", AccountActive, LevelIncidents)
	add("66666", AccountActive, LevelAgentInfo)
	add("77777", AccountDisabled, LevelView)
	add("88888", AccountPending, 0)
	add("99999", AccountPending, 0)
	add("12121", AccountActive, LevelCluster) // mức 1 khác

	var c *http.Cookie
	post(h, "/auth/email/code", accountReq{Username: "11111"})
	_, _, cs := post(h, "/auth/email/login", accountReq{Username: "11111", Code: codeOf(mail.next(t))})
	if c = sessionCookie(cs); c == nil {
		t.Fatal("mức 1 không đăng nhập được")
	}

	// Danh sách: chỉ chờ duyệt + mức 5–7, không có chính mình, assign_min = 5.
	w := req(h, "GET", "/admin/v1/accounts", "", nil, c)
	var j struct {
		Accounts  []Account `json:"accounts"`
		AssignMin int       `json:"assign_min"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	seen := map[string]bool{}
	for _, a := range j.Accounts {
		seen[a.Username] = true
	}
	if w.Code != 200 || j.AssignMin != 5 || len(seen) != 5 || !seen["88888"] || !seen["55555"] || !seen["77777"] ||
		seen["22222"] || seen["33333"] || seen["11111"] || seen["12121"] {
		t.Fatalf("danh sách mức 1: %d assign_min=%d %v", w.Code, j.AssignMin, seen)
	}

	act := func(user, action string, lv Level) int {
		code, _, _ := post(h, "/admin/v1/accounts/"+user, map[string]any{"action": action, "level": lv}, c)
		return code
	}
	deny := []struct {
		user, action string
		lv           Level
	}{
		{"88888", "approve", LevelServers}, // cấp mức 4
		{"88888", "approve", LevelCluster}, // cấp mức 1
		{"66666", "level", LevelAgents},    // nâng 6 → 3
		{"33333", "level", LevelView},      // hạ người mức 3
		{"33333", "disable", 0},            // khóa người mức 3
		{"22222", "delete", 0},             // xóa người mức 2
		{"12121", "disable", 0},            // khóa mức 1 khác
		{"11111", "level", LevelView},      // tự sửa mình
		{"66666", "delete", 0},             // xóa tài khoản đang dùng
		{"66666", "session", 0},            // giới hạn phiên
		{"88888", "level", LevelIncidents}, // đổi mức tài khoản chờ (phải duyệt)
	}
	before := map[string]Account{}
	for k, v := range st.m {
		before[k] = v
	}
	for _, d := range deny {
		if code := act(d.user, d.action, d.lv); code != 403 {
			t.Errorf("mức 1 %s %s mức %d: %d, phải 403", d.action, d.user, d.lv, code)
		}
	}
	for k, v := range before {
		if st.m[k] != v {
			t.Errorf("thao tác bị từ chối không được đổi dữ liệu: %s %+v → %+v", k, v, st.m[k])
		}
	}
	// Được phép.
	if code := act("88888", "approve", LevelIncidents); code != 200 || st.m["88888"].Level != LevelIncidents || st.m["88888"].Status != AccountActive {
		t.Fatalf("duyệt mức 5: %d %+v", code, st.m["88888"])
	}
	mail.next(t) // thư báo đã duyệt
	if code := act("66666", "level", LevelView); code != 200 || st.m["66666"].Level != LevelView {
		t.Fatalf("đổi 6 → 7: %d", code)
	}
	if code := act("55555", "disable", 0); code != 200 || st.m["55555"].Status != AccountDisabled {
		t.Fatalf("khóa mức 5: %d", code)
	}
	if code := act("77777", "enable", 0); code != 200 || st.m["77777"].Status != AccountActive {
		t.Fatalf("mở khóa mức 7: %d", code)
	}
	if code := act("99999", "delete", 0); code != 200 {
		t.Fatalf("từ chối tài khoản chờ: %d", code)
	}
	if _, ok := st.m["99999"]; ok {
		t.Fatal("tài khoản chờ phải bị xóa")
	}
	// Mức 1 không xem / thu hồi phiên (vẫn chỉ quản trị gốc).
	if w := req(h, "GET", "/admin/v1/sessions", "", nil, c); w.Code != 403 {
		t.Fatalf("mức 1 xem phiên: %d", w.Code)
	}
	// Quản trị gốc vẫn làm được mọi thứ.
	rc := rootCookie(t, m, h)
	if code, _, _ := post(h, "/admin/v1/accounts/33333", map[string]any{"action": "level", "level": 2}, rc); code != 200 {
		t.Fatalf("quản trị gốc đổi mức: %d", code)
	}
	// Mức 1 gỡ khóa đăng nhập được.
	m.block(BlockIP, "203.0.113.99", "203.0.113.99", "thử", time.Hour)
	if code, _, _ := post(h, "/admin/v1/login-blocks/remove", map[string]any{"kind": "ip", "key": "203.0.113.99"}, c); code != 200 || len(m.ListBlocks()) != 0 {
		t.Fatalf("mức 1 gỡ khóa: %d", code)
	}
}

package auth

import (
	"net/http"
	"testing"
	"time"
)

// Phiên tài khoản: sống đúng ttl; đủ max phiên thì đăng nhập mới cắt phiên cũ nhất; đổi giới hạn thì rút
// hạn và cắt bớt phiên đang mở, không cắt hết.
func TestSessionTTLAndCap(t *testing.T) {
	now := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	s := newSessions(func() time.Time { return now })
	var cookies []string
	for i := 0; i < 3; i++ {
		c, ss, ev := s.createLevel("10123", "10123", LevelView, "1.2.3.4", "", 2*time.Hour, 2)
		if !ss.Expires.Equal(now.Add(2 * time.Hour)) {
			t.Fatalf("hạn phiên: %s", ss.Expires)
		}
		if want := map[int]int{0: 0, 1: 0, 2: 1}[i]; ev != want {
			t.Fatalf("lần %d: cắt %d phiên, muốn %d", i, ev, want)
		}
		cookies = append(cookies, c)
		now = now.Add(time.Minute)
	}
	if _, ok := s.get(cookies[0], ""); ok {
		t.Fatal("phiên cũ nhất phải bị cắt khi vượt 2 phiên")
	}
	if _, ok := s.get(cookies[2], ""); !ok {
		t.Fatal("phiên mới phải còn")
	}
	// Phiên tài khoản khác không bị ảnh hưởng.
	other, _, _ := s.createLevel("10456", "10456", LevelView, "", "", time.Hour, 1)
	// Rút xuống 1 giờ, 1 phiên: phiên mới nhất còn, hạn = lúc đăng nhập + 1 giờ.
	if n := s.applyLimits("10123", time.Hour, 1); n != 1 {
		t.Fatalf("áp giới hạn: cắt %d", n)
	}
	ss, ok := s.get(cookies[2], "")
	if !ok || !ss.Expires.Equal(ss.Created.Add(time.Hour)) {
		t.Fatalf("phiên còn lại phải rút hạn: %+v %v", ss, ok)
	}
	if _, ok := s.get(other, ""); !ok {
		t.Fatal("không được đụng phiên tài khoản khác")
	}
	now = now.Add(2 * time.Hour)
	if _, ok := s.get(cookies[2], ""); ok {
		t.Fatal("quá hạn phải hết phiên")
	}
}

func TestSessionLimitsRange(t *testing.T) {
	for _, c := range []struct {
		h, n int
		ok   bool
	}{{1, 1, true}, {360, 20, true}, {24, 4, true}, {0, 4, false}, {361, 4, false}, {24, 0, false}, {24, 21, false}} {
		if ValidSessionLimits(c.h, c.n) != c.ok {
			t.Errorf("%d giờ, %d phiên: muốn %v", c.h, c.n, c.ok)
		}
	}
	ttl, n := Account{}.sessionLimits()
	if ttl != 24*time.Hour || n != 4 {
		t.Fatalf("mặc định 24 giờ, 4 phiên: %s %d", ttl, n)
	}
}

// API: chỉ quản trị gốc đổi giới hạn phiên; giá trị ngoài khoảng bị chặn; đăng ký chỉ nhận chữ số.
func TestSessionLimitsAPIAndDigitsOnly(t *testing.T) {
	m, h, st, _ := emailSetup(t)
	st.m["10123"] = Account{Username: "10123", Email: "a@gmail.com", Status: AccountActive, Level: LevelAgents,
		SessionHours: 24, MaxSessions: 4}
	root := rootCookie(t, m, h)
	cookie, _, _ := m.sess.createLevel("10123", "10123", LevelAgents, "", "", 24*time.Hour, 4)
	user := &http.Cookie{Name: CookieName, Value: cookie}

	body := map[string]any{"action": "session", "session_hours": 2, "max_sessions": 1}
	if code, _, _ := post(h, "/admin/v1/accounts/10123", body, user); code != 403 {
		t.Fatalf("mức 3 không được đổi giới hạn phiên: %d", code)
	}
	for _, bad := range []map[string]any{
		{"action": "session", "session_hours": 0, "max_sessions": 1},
		{"action": "session", "session_hours": 361, "max_sessions": 1},
		{"action": "session", "session_hours": 24, "max_sessions": 0},
		{"action": "session", "session_hours": 24, "max_sessions": 21},
	} {
		if code, _, _ := post(h, "/admin/v1/accounts/10123", bad, root); code != 400 {
			t.Fatalf("giá trị sai %v phải 400, được %d", bad, code)
		}
	}
	if code, _, _ := post(h, "/admin/v1/accounts/10123", map[string]any{"action": "session", "session_hours": 360, "max_sessions": 2}, root); code != 200 {
		t.Fatalf("quản trị gốc đặt 15 ngày, 2 phiên: %d", code)
	}
	if a := st.m["10123"]; a.SessionHours != 360 || a.MaxSessions != 2 {
		t.Fatalf("phải lưu: %+v", a)
	}
	if _, ok := m.sess.get(cookie, ""); !ok {
		t.Fatal("đổi giới hạn phiên không cắt phiên đang mở (còn trong giới hạn)")
	}

	for _, name := range []string{"an.it", "abc12", "123-45", "12 34", "１２３"} {
		if code, _, _ := post(h, "/auth/register", accountReq{Username: name, Email: "x@gmail.com"}); code != 400 {
			t.Errorf("đăng ký %q phải bị chặn (chỉ chữ số), được %d", name, code)
		}
	}
	if code, _, _ := post(h, "/auth/register", accountReq{Username: "20001", Email: "x@gmail.com"}); code != 200 {
		t.Fatalf("đăng ký toàn số: %d", code)
	}
}

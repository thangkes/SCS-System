package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// Phiên bị cắt sau tối đa ngần này kể từ lúc đăng nhập, không gia hạn (giống NASfone). Tài khoản email
// có thời gian sống và số phiên riêng do quản trị gốc đặt (Account.SessionHours, MaxSessions).
const sessionMaxAge = 24 * time.Hour

// Session là một phiên đăng nhập trình duyệt.
type Session struct {
	ID       string    `json:"id"` // mã công khai để hiển thị/thu hồi, không dùng được làm cookie
	Name     string    `json:"name"`
	Role     Role      `json:"role"`
	Level    Level     `json:"level"`             // 0 = quản trị gốc … 7 = chỉ xem
	Account  string    `json:"account,omitempty"` // tài khoản email (rỗng = đăng nhập bằng mã SSH / token)
	IP       string    `json:"ip"`
	Agent    string    `json:"agent"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
	Expires  time.Time `json:"expires"`
}

// sessions lưu trong RAM theo SHA-256 của cookie: đọc được bộ nhớ hay log cũng
// không lấy lại được cookie. Khởi động lại server thì mọi người phải đăng nhập lại.
type sessions struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]*Session // khóa: sha256(cookie)
}

func newSessions(now func() time.Time) *sessions {
	return &sessions{now: now, m: map[string]*Session{}}
}

// create: phiên đăng nhập bằng mã SSH (admin = quản trị gốc, viewer = chỉ xem).
func (s *sessions) create(name string, role Role, ip, agent string) (string, Session) {
	lv := LevelView
	if role == RoleAdmin {
		lv = LevelRoot
	}
	return s.add(&Session{Name: name, Role: role, Level: lv}, ip, agent)
}

// createLevel: phiên của tài khoản email với mức quyền, thời gian sống ttl và số phiên tối đa max của tài
// khoản. Đã đủ max phiên thì cắt phiên cũ nhất (trả số phiên bị cắt).
func (s *sessions) createLevel(name, account string, lv Level, ip, agent string, ttl time.Duration, max int) (string, Session, int) {
	role := RoleViewer
	if lv < LevelView {
		role = RoleAdmin // có quyền thay đổi ít nhất một thứ
	}
	ss := &Session{Name: name, Role: role, Level: lv, Account: account}
	return s.addTTL(ss, ip, agent, ttl, max-1)
}

func (s *sessions) add(ss *Session, ip, agent string) (cookie string, sess Session) {
	cookie, sess, _ = s.addTTL(ss, ip, agent, sessionMaxAge, -1)
	return cookie, sess
}

// addTTL thêm phiên sống ttl. keep >= 0: trước khi thêm, chỉ giữ keep phiên mới nhất của cùng tài khoản
// (trả số phiên bị cắt).
func (s *sessions) addTTL(ss *Session, ip, agent string, ttl time.Duration, keep int) (cookie string, sess Session, evicted int) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	cookie = base64.RawURLEncoding.EncodeToString(b)
	key := hashKey(cookie)
	now := s.now()
	ss.ID, ss.IP, ss.Agent = key[:12], ip, agent
	ss.Created, ss.LastSeen, ss.Expires = now, now, now.Add(ttl)
	s.mu.Lock()
	s.sweep(now) // dọn phiên hết hạn mỗi lần có đăng nhập mới: số phiên trong RAM không tăng mãi
	if keep >= 0 && ss.Account != "" {
		evicted = s.trimAccount(ss.Account, keep)
	}
	s.m[key] = ss
	sess = *ss
	s.mu.Unlock()
	return cookie, sess, evicted
}

// trimAccount chỉ giữ keep phiên mới nhất của tài khoản, cắt phiên cũ hơn. Gọi khi đang giữ s.mu.
func (s *sessions) trimAccount(account string, keep int) int {
	type kv struct {
		k string
		t time.Time
	}
	var mine []kv
	for k, ss := range s.m {
		if ss.Account == account {
			mine = append(mine, kv{k, ss.Created})
		}
	}
	if len(mine) <= keep {
		return 0
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].t.After(mine[j].t) })
	for _, e := range mine[keep:] {
		delete(s.m, e.k)
	}
	return len(mine) - keep
}

// applyLimits áp giới hạn mới cho các phiên đang mở của tài khoản: hạn không vượt lúc đăng nhập + ttl, chỉ
// giữ max phiên mới nhất. Không cắt hết (máy đang dùng, BOT đang chạy vẫn tiếp tục). Trả số phiên bị cắt.
func (s *sessions) applyLimits(account string, ttl time.Duration, max int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.m {
		if ss.Account == account {
			if e := ss.Created.Add(ttl); e.Before(ss.Expires) {
				ss.Expires = e
			}
		}
	}
	s.sweep(s.now())
	return s.trimAccount(account, max)
}

// sweep xóa các phiên đã hết hạn. Gọi khi đang giữ s.mu.
func (s *sessions) sweep(now time.Time) {
	for k, ss := range s.m {
		if !now.Before(ss.Expires) {
			delete(s.m, k)
		}
	}
}

// get trả phiên còn hạn ứng với cookie, cập nhật lần cuối hoạt động.
func (s *sessions) get(cookie, ip string) (Session, bool) {
	if cookie == "" {
		return Session{}, false
	}
	key := hashKey(cookie)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.m[key]
	if !ok {
		return Session{}, false
	}
	if !now.Before(ss.Expires) {
		delete(s.m, key)
		return Session{}, false
	}
	ss.LastSeen, ss.IP = now, ip
	return *ss, true
}

func (s *sessions) deleteCookie(cookie string) (Session, bool) {
	key := hashKey(cookie)
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.m[key]
	if ok {
		delete(s.m, key)
		return *ss, true
	}
	return Session{}, false
}

// revoke thu hồi phiên theo ID công khai.
func (s *sessions) revoke(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, ss := range s.m {
		if ss.ID == id {
			delete(s.m, k)
			return *ss, true
		}
	}
	return Session{}, false
}

// revokeAccount cắt mọi phiên của một tài khoản email (đổi quyền, khóa, xóa). Trả số phiên bị cắt.
func (s *sessions) revokeAccount(account string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, ss := range s.m {
		if ss.Account != "" && ss.Account == account {
			delete(s.m, k)
			n++
		}
	}
	return n
}

// list trả các phiên còn hạn, mới nhất trước.
func (s *sessions) list() []Session {
	now := s.now()
	s.mu.Lock()
	out := make([]Session, 0, len(s.m))
	for k, ss := range s.m {
		if !now.Before(ss.Expires) {
			delete(s.m, k)
			continue
		}
		out = append(out, *ss)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

func hashKey(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:])
}

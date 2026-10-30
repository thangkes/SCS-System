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

// Phiên bị cắt sau tối đa ngần này kể từ lúc đăng nhập, không gia hạn (giống NASfone).
const sessionMaxAge = 24 * time.Hour

// Session là một phiên đăng nhập trình duyệt.
type Session struct {
	ID       string    `json:"id"` // mã công khai để hiển thị/thu hồi, không dùng được làm cookie
	Name     string    `json:"name"`
	Role     Role      `json:"role"`
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

func (s *sessions) create(name string, role Role, ip, agent string) (cookie string, sess Session) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	cookie = base64.RawURLEncoding.EncodeToString(b)
	key := hashKey(cookie)
	now := s.now()
	ss := &Session{
		ID: key[:12], Name: name, Role: role, IP: ip, Agent: agent,
		Created: now, LastSeen: now, Expires: now.Add(sessionMaxAge),
	}
	s.mu.Lock()
	s.sweep(now) // dọn phiên hết hạn mỗi lần có đăng nhập mới: số phiên trong RAM không tăng mãi
	s.m[key] = ss
	s.mu.Unlock()
	return cookie, *ss
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

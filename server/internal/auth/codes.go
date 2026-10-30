// Package auth: đăng nhập web quản trị bằng mã 6 số (giống NASfone), phiên cookie, phân quyền.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// Role là quyền của một phiên.
type Role string

const (
	RoleAdmin  Role = "admin"  // toàn quyền: duyệt, từ chối, thu hồi máy
	RoleViewer Role = "viewer" // chỉ xem
)

var roles = []Role{RoleAdmin, RoleViewer}

const (
	codeGrace     = 10 * time.Second // mã cũ còn dùng được thêm sau khi đổi
	wrongToRotate = 5                // sai liên tiếp bấy nhiêu lần thì đổi mã sớm
)

type code struct {
	value string
	until time.Time // hết hiệu lực (với mã cũ: đã cộng thời gian ân hạn)
}

// Codes giữ hai mã đăng nhập (Admin, Chỉ xem), tự đổi vào đầu mỗi phút.
//
// Bất biến: tại mọi thời điểm, các mã còn hiệu lực (mã hiện tại và mã cũ đang
// ân hạn của cả hai quyền) đều khác nhau, nên một mã nhập vào chỉ khớp tối đa
// một quyền.
type Codes struct {
	mu    sync.Mutex
	now   func() time.Time
	gen   func() string
	cur   map[Role]code
	prev  map[Role]code
	wrong int

	// OnRotateEarly được gọi (ngoài khóa) khi đổi mã sớm do nhập sai nhiều.
	OnRotateEarly func()
}

// NewCodes tạo bộ mã dùng đồng hồ thật và crypto/rand.
func NewCodes() *Codes { return newCodes(time.Now, randomCode) }

func newCodes(now func() time.Time, gen func() string) *Codes {
	c := &Codes{now: now, gen: gen, cur: map[Role]code{}, prev: map[Role]code{}}
	c.mu.Lock()
	for _, r := range roles {
		c.cur[r] = c.fresh(nextMinute(now()))
	}
	c.mu.Unlock()
	return c
}

// Current là mã đang hiện hành, dùng cho lệnh `scs-server login-code`.
type Current struct {
	Admin  string    `json:"admin"`
	Viewer string    `json:"viewer"`
	Until  time.Time `json:"until"` // thời điểm đổi mã theo lịch
	// LockedUntil: đăng nhập đang bị khóa tới lúc này (zero = không khóa). Codes.Snapshot
	// không điền; Manager.Snapshot điền.
	LockedUntil time.Time `json:"locked_until"`
}

// Snapshot trả mã hiện hành của cả hai quyền.
func (c *Codes) Snapshot() Current {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick()
	return Current{Admin: c.cur[RoleAdmin].value, Viewer: c.cur[RoleViewer].value, Until: c.cur[RoleAdmin].until}
}

// Use kiểm mã người dùng nhập. Đúng thì trả quyền và mã đó hết hiệu lực ngay
// (chỉ mã của quyền đó được đổi). Sai thì đếm; đủ 5 lần sai thì đổi mã cả hai quyền.
func (c *Codes) Use(input string) (Role, bool) {
	c.mu.Lock()
	c.tick()
	now := c.now()

	var hit []struct {
		r    Role
		prev bool
	}
	for _, r := range roles {
		if match(c.cur[r], input, now) {
			hit = append(hit, struct {
				r    Role
				prev bool
			}{r, false})
		}
		if match(c.prev[r], input, now) {
			hit = append(hit, struct {
				r    Role
				prev bool
			}{r, true})
		}
	}

	// Khớp từ hai mã trở lên không thể xảy ra (bất biến ở trên); nếu có thì từ chối.
	if len(hit) != 1 {
		early := c.countWrong()
		c.mu.Unlock()
		if early && c.OnRotateEarly != nil {
			c.OnRotateEarly()
		}
		return "", false
	}

	h := hit[0]
	if h.prev {
		delete(c.prev, h.r)
	} else {
		c.cur[h.r] = c.fresh(c.cur[h.r].until)
	}
	c.wrong = 0
	c.mu.Unlock()
	return h.r, true
}

// countWrong tăng bộ đếm sai; đủ ngưỡng thì đổi mã cả hai quyền, bỏ luôn mã cũ.
func (c *Codes) countWrong() bool {
	c.wrong++
	if c.wrong < wrongToRotate {
		return false
	}
	c.wrong = 0
	c.prev = map[Role]code{}
	for _, r := range roles {
		c.cur[r] = c.fresh(c.cur[r].until)
	}
	return true
}

// tick đổi mã khi sang phút mới: mã hiện tại thành mã cũ (ân hạn 10 giây).
//
// Hạn của mã cắt theo phút đồng hồ hệ thống (không có đồng hồ đơn điệu), nên đồng hồ bị
// lùi (NTP chỉnh, máy ảo chạy lại) sẽ làm "phút kế tiếp" còn rất xa và mã không đổi suốt
// khoảng bị lùi. Hạn còn xa hơn một phút nghĩa là đồng hồ đã lùi: bỏ mọi mã, sinh mã mới.
func (c *Codes) tick() {
	now := c.now()
	if c.cur[RoleAdmin].until.Sub(now) > time.Minute {
		c.prev = map[Role]code{}
		c.cur = map[Role]code{} // để fresh() không né mã cũ đã bỏ
		for _, r := range roles {
			c.cur[r] = c.fresh(nextMinute(now))
		}
		return
	}
	for _, r := range roles {
		if p, ok := c.prev[r]; ok && !now.Before(p.until) {
			delete(c.prev, r)
		}
	}
	if now.Before(c.cur[RoleAdmin].until) {
		return
	}
	next := nextMinute(now)
	old := c.cur
	c.cur = map[Role]code{}
	for _, r := range roles {
		c.prev[r] = code{value: old[r].value, until: old[r].until.Add(codeGrace)}
		if !now.Before(c.prev[r].until) { // đã quá cả thời gian ân hạn
			delete(c.prev, r)
		}
	}
	for _, r := range roles {
		c.cur[r] = c.fresh(next)
	}
}

// fresh sinh mã mới khác mọi mã đang còn hiệu lực.
func (c *Codes) fresh(until time.Time) code {
	for {
		v := c.gen()
		if !c.inUse(v) {
			return code{value: v, until: until}
		}
	}
}

func (c *Codes) inUse(v string) bool {
	for _, m := range []map[Role]code{c.cur, c.prev} {
		for _, x := range m {
			if x.value == v {
				return true
			}
		}
	}
	return false
}

func match(x code, input string, now time.Time) bool {
	if x.value == "" || !now.Before(x.until) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(x.value), []byte(input)) == 1
}

func nextMinute(t time.Time) time.Time { return t.Truncate(time.Minute).Add(time.Minute) }

func randomCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err) // crypto/rand hỏng thì không thể tiếp tục an toàn
	}
	return fmt.Sprintf("%06d", n.Int64())
}

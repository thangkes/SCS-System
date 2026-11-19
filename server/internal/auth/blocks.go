package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// Khóa đăng nhập theo IP / theo thiết bị (chống dò mã và dò tên tài khoản):
//
//   - Nhập sai mã 5 lần liên tiếp cho cùng một tài khoản (mã gửi qua email, mã đăng ký, mã SSH của quản trị
//     gốc) → khóa IP 24 giờ. Nhập đúng thì đếm lại từ đầu.
//   - Xin mã cho tên không có / không hoạt động, hoặc đăng ký sai định dạng, quá 10 lần trong 24 giờ
//     → khóa IP 7 ngày.
//   - IP thuộc siêu thị (IP WAN đã nạp, hoặc IP mà agent đã duyệt đang gửi về) có nhiều người dùng chung:
//     không khóa cả IP mà khóa theo MÃ THIẾT BỊ (cookie ngẫu nhiên server cấp cho từng trình duyệt).
//     Xóa cookie để lách thì đụng trần: một IP siêu thị có quá 3 thiết bị bị khóa trong 24 giờ → khóa IP 24 giờ.
//
// Chỉ chặn đăng nhập / đăng ký / xin mã; phiên đang mở vẫn dùng tiếp. Danh sách khóa lưu DB (sống qua khởi
// động lại); bộ đếm lần sai chỉ trong RAM. Quản trị gốc và mức 1 mở khóa trên web; trên máy server:
// `scs-server login-code -unlock` mở mọi khóa.
const (
	codeFailMax     = 5
	codeFailBlock   = 24 * time.Hour
	userMissMax     = 10
	userMissWindow  = 24 * time.Hour
	userMissBlock   = 7 * 24 * time.Hour
	siteDevMax      = 3
	siteDevWindow   = 24 * time.Hour
	siteDevIPBlock  = 24 * time.Hour
	siteIPCacheFor  = 5 * time.Minute
	maxGuardEntries = 20000 // chặn phình RAM khi bị dò từ quá nhiều nguồn
	DeviceCookie    = "scs_dev"
	deviceCookieAge = 365 * 24 * time.Hour
)

// Loại khóa.
const (
	BlockIP     = "ip"
	BlockDevice = "device"
)

// Block: một khóa đăng nhập đang hiệu lực.
type Block struct {
	Kind    string    `json:"kind"` // ip | device
	Key     string    `json:"key"`  // địa chỉ IP, hoặc mã thiết bị
	IP      string    `json:"ip"`   // IP lúc bị khóa (khóa thiết bị: IP siêu thị)
	Reason  string    `json:"reason"`
	Until   time.Time `json:"until"`
	Created time.Time `json:"created"`
}

// BlockStore lưu khóa đăng nhập để sống qua khởi động lại (PostgreSQL ở internal/store). nil = chỉ trong RAM.
type BlockStore interface {
	LoadBlocks(ctx context.Context) ([]Block, error) // khóa còn hạn
	SaveBlock(ctx context.Context, b Block) error    // thêm / gia hạn
	// DeleteBlocks gỡ khóa (kind và key rỗng = gỡ hết), by = người làm (ghi nhật ký thao tác).
	DeleteBlocks(ctx context.Context, kind, key, by string) error
}

// subject: "ai" đang thử đăng nhập — IP, hoặc mã thiết bị khi IP thuộc siêu thị.
type subject struct {
	kind, key, ip string
	site          bool
}

func (s subject) id() string { return s.kind + "|" + s.key }

type failRun struct {
	n    int
	last time.Time
}

type siteIP struct {
	site bool
	at   time.Time
}

// guard giữ khóa và bộ đếm lần sai.
type guard struct {
	mu       sync.Mutex
	now      func() time.Time
	blocks   map[string]Block       // kind|key → khóa
	codeFail map[string]*failRun    // subject|tài khoản → số lần sai mã liên tiếp
	userMiss map[string][]time.Time // subject → các lần sai tên / đăng ký sai
	siteDevs map[string][]time.Time // IP siêu thị → các lần khóa thiết bị
	siteIPs  map[string]siteIP      // bộ nhớ đệm "IP này có thuộc siêu thị không"
}

func newGuard(now func() time.Time) *guard {
	return &guard{now: now, blocks: map[string]Block{}, codeFail: map[string]*failRun{},
		userMiss: map[string][]time.Time{}, siteDevs: map[string][]time.Time{}, siteIPs: map[string]siteIP{}}
}

// LoadBlocks nạp khóa còn hạn từ DB (gọi lúc khởi động).
func (m *Manager) LoadBlocks(ctx context.Context) error {
	if m.Blocks == nil {
		return nil
	}
	list, err := m.Blocks.LoadBlocks(ctx)
	if err != nil {
		return err
	}
	g := m.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, b := range list {
		g.blocks[b.Kind+"|"+b.Key] = b
	}
	return nil
}

// deviceID đọc mã thiết bị từ cookie (32 ký tự hex); không có / sai dạng → "".
func deviceID(r *http.Request) string {
	c, err := r.Cookie(DeviceCookie)
	if err != nil || len(c.Value) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(c.Value); err != nil {
		return ""
	}
	return c.Value
}

// ensureDevice cấp mã thiết bị cho trình duyệt chưa có (cookie HttpOnly, 1 năm, chỉ gửi cho /auth).
func ensureDevice(w http.ResponseWriter, r *http.Request) {
	if deviceID(r) != "" {
		return
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: DeviceCookie, Value: hex.EncodeToString(b), Path: "/auth",
		MaxAge: int(deviceCookieAge.Seconds()), HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

// isSite: IP có thuộc siêu thị không (hỏi DB, nhớ 5 phút).
func (m *Manager) isSite(ctx context.Context, ip string) bool {
	if m.SiteIP == nil {
		return false
	}
	g := m.guard
	now := g.now()
	g.mu.Lock()
	c, ok := g.siteIPs[ip]
	g.mu.Unlock()
	if ok && now.Sub(c.at) < siteIPCacheFor {
		return c.site
	}
	site, err := m.SiteIP(ctx, ip)
	if err != nil {
		m.log.Error("không kiểm được IP có thuộc siêu thị không", "ip", ip, "err", err)
		return ok && c.site
	}
	g.mu.Lock()
	if len(g.siteIPs) > maxGuardEntries {
		g.siteIPs = map[string]siteIP{}
	}
	g.siteIPs[ip] = siteIP{site, now}
	g.mu.Unlock()
	return site
}

func (m *Manager) subjectOf(r *http.Request) subject {
	ip := clientIP(r)
	if !m.isSite(r.Context(), ip) {
		return subject{kind: BlockIP, key: ip, ip: ip}
	}
	dev := deviceID(r)
	if dev == "" {
		dev = "khong-cookie@" + ip // trình duyệt thật luôn có cookie (cấp khi mở trang đăng nhập)
	}
	return subject{kind: BlockDevice, key: dev, ip: ip, site: true}
}

// blockedUntil: người gọi đang bị khóa tới lúc nào (zero = không) và khóa đó là khóa IP hay thiết bị. Khóa
// IP áp cả cho IP siêu thị (trần).
func (g *guard) blockedUntil(s subject) (time.Time, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	var until time.Time
	kind := ""
	for _, id := range []string{BlockIP + "|" + s.ip, s.id()} {
		b, ok := g.blocks[id]
		if !ok {
			continue
		}
		if !now.Before(b.Until) {
			delete(g.blocks, id)
			continue
		}
		if b.Until.After(until) {
			until, kind = b.Until, b.Kind
		}
	}
	return until, kind
}

// checkBlocked trả true (và đã ghi phản hồi 403) khi người gọi đang bị khóa.
func (m *Manager) checkBlocked(w http.ResponseWriter, r *http.Request, s subject) bool {
	until, kind := m.guard.blockedUntil(s)
	if until.IsZero() {
		return false
	}
	what := "IP này"
	if kind == BlockDevice {
		what = "Thiết bị này"
	}
	httpx.WriteJSON(w, http.StatusForbidden, map[string]any{"ok": false, "blocked_until": until,
		"error": fmt.Sprintf("%s bị khóa đăng nhập / đăng ký tới %s do nhập sai nhiều lần; liên hệ quản trị để mở khóa",
			what, until.Local().Format("15:04 02/01/2006"))})
	return true
}

// block thêm khóa (giữ khóa dài hơn nếu đã có), ghi DB và log. Gọi khi KHÔNG giữ g.mu.
func (m *Manager) block(kind, key, ip, reason string, dur time.Duration) {
	g := m.guard
	now := g.now()
	b := Block{Kind: kind, Key: key, IP: ip, Reason: reason, Until: now.Add(dur), Created: now}
	g.mu.Lock()
	if old, ok := g.blocks[kind+"|"+key]; ok && old.Until.After(b.Until) {
		g.mu.Unlock()
		return
	}
	g.blocks[kind+"|"+key] = b
	g.mu.Unlock()
	m.log.Error("khóa đăng nhập web quản trị", "loai", kind, "khoa", key, "ip", ip, "ly_do", reason,
		"toi", b.Until.Format(time.DateTime))
	if m.Blocks != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Blocks.SaveBlock(ctx, b); err != nil {
			m.log.Error("không lưu được khóa đăng nhập vào DB (vẫn khóa trong RAM)", "err", err)
		}
	}
	if kind == BlockDevice {
		m.siteDeviceBlocked(ip)
	}
}

// siteDeviceBlocked: trần cho IP siêu thị — quá 3 thiết bị bị khóa trong 24 giờ thì khóa cả IP 24 giờ.
func (m *Manager) siteDeviceBlocked(ip string) {
	g := m.guard
	now := g.now()
	g.mu.Lock()
	list := append(prune(g.siteDevs[ip], now, siteDevWindow), now)
	trip := len(list) > siteDevMax
	if trip {
		delete(g.siteDevs, ip)
	} else {
		g.siteDevs[ip] = list
	}
	g.mu.Unlock()
	if trip {
		m.block(BlockIP, ip, ip, fmt.Sprintf("IP siêu thị có quá %d thiết bị bị khóa trong 24 giờ", siteDevMax), siteDevIPBlock)
	}
}

// codeFailed: nhập sai mã cho tài khoản user. Đủ 5 lần liên tiếp → khóa 24 giờ.
func (m *Manager) codeFailed(s subject, user string) {
	g := m.guard
	now := g.now()
	k := s.id() + "|" + user
	g.mu.Lock()
	if len(g.codeFail) > maxGuardEntries {
		for key, f := range g.codeFail {
			if now.Sub(f.last) > codeFailBlock {
				delete(g.codeFail, key)
			}
		}
	}
	f := g.codeFail[k]
	if f == nil || now.Sub(f.last) > codeFailBlock {
		f = &failRun{}
		g.codeFail[k] = f
	}
	f.n++
	f.last = now
	trip := f.n >= codeFailMax
	if trip {
		delete(g.codeFail, k)
	}
	g.mu.Unlock()
	if trip {
		m.block(s.kind, s.key, s.ip, fmt.Sprintf("nhập sai mã %d lần liên tiếp cho tài khoản %s", codeFailMax, user), codeFailBlock)
	}
}

// codeOK: nhập đúng mã → đếm lại từ đầu.
func (m *Manager) codeOK(s subject, user string) {
	g := m.guard
	g.mu.Lock()
	delete(g.codeFail, s.id()+"|"+user)
	g.mu.Unlock()
}

// userMissed: tên không có / không hoạt động, hoặc đăng ký sai định dạng. Quá 10 lần / 24 giờ → khóa 7 ngày.
func (m *Manager) userMissed(s subject, what string) {
	g := m.guard
	now := g.now()
	g.mu.Lock()
	if _, ok := g.userMiss[s.id()]; !ok && len(g.userMiss) > maxGuardEntries {
		for key, v := range g.userMiss {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > userMissWindow {
				delete(g.userMiss, key)
			}
		}
	}
	list := append(prune(g.userMiss[s.id()], now, userMissWindow), now)
	trip := len(list) > userMissMax
	if trip {
		delete(g.userMiss, s.id())
	} else {
		g.userMiss[s.id()] = list
	}
	g.mu.Unlock()
	if trip {
		m.block(s.kind, s.key, s.ip, fmt.Sprintf("quá %d lần sai tên đăng nhập / đăng ký sai trong 24 giờ (lần cuối: %s)", userMissMax, what), userMissBlock)
	}
}

// ListBlocks: khóa đang hiệu lực, mới nhất trước.
func (m *Manager) ListBlocks() []Block {
	g := m.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	out := make([]Block, 0, len(g.blocks))
	for id, b := range g.blocks {
		if !now.Before(b.Until) {
			delete(g.blocks, id)
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Unblock gỡ một khóa (kind, key) hoặc mọi khóa (kind = ""), xóa luôn bộ đếm lần sai liên quan.
func (m *Manager) Unblock(ctx context.Context, kind, key, by string) (int, error) {
	g := m.guard
	g.mu.Lock()
	n := 0
	for id, b := range g.blocks {
		if kind == "" || (b.Kind == kind && b.Key == key) {
			delete(g.blocks, id)
			n++
		}
	}
	if kind == "" {
		g.codeFail, g.userMiss, g.siteDevs = map[string]*failRun{}, map[string][]time.Time{}, map[string][]time.Time{}
	} else {
		id := kind + "|" + key
		delete(g.userMiss, id)
		for k := range g.codeFail {
			if len(k) > len(id) && k[:len(id)+1] == id+"|" {
				delete(g.codeFail, k)
			}
		}
		if kind == BlockIP {
			delete(g.siteDevs, key)
		}
	}
	g.mu.Unlock()
	if m.Blocks != nil {
		if err := m.Blocks.DeleteBlocks(ctx, kind, key, by); err != nil {
			return n, err
		}
	}
	return n, nil
}

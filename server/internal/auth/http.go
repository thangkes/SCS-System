package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// CookieName là tên cookie phiên của web quản trị.
const CookieName = "scs_session"

// Manager gom mã đăng nhập, giới hạn thử sai và phiên.
type Manager struct {
	Codes *Codes
	lim   *limiter
	sess  *sessions
	log   *slog.Logger
	// apiToken (SCS_ADMIN_TOKEN) cho script tự động gọi API bằng Bearer; giao diện web không dùng.
	apiToken [32]byte
	hasToken bool

	// Tài khoản đăng nhập bằng mã gửi qua email (nil = tắt).
	Accounts     AccountStore
	Mail         Mailer
	EmailDomains []string // tên miền email được đăng ký (rỗng = mọi tên miền)
	email        *emailAuth

	// Đăng nhập bằng app xác thực (TOTP, totpflow.go): TOTP nil hoặc TOTPKeys nil = tắt.
	TOTP     *TOTPSealer
	TOTPKeys TOTPStore
	totp     *totpAuth

	// Khóa đăng nhập theo IP / thiết bị (blocks.go). Blocks nil = chỉ trong RAM; SiteIP nil = không có IP
	// siêu thị (luôn khóa theo IP).
	Blocks BlockStore
	SiteIP func(ctx context.Context, ip string) (bool, error)
	guard  *guard
}

// NewManager tạo bộ đăng nhập. apiToken rỗng thì không nhận Bearer token.
func NewManager(apiToken string, log *slog.Logger) *Manager {
	m := &Manager{Codes: NewCodes(), lim: newLimiter(time.Now), sess: newSessions(time.Now), log: log, email: newEmailAuth(),
		guard: newGuard(time.Now), totp: newTOTPAuth()}
	if apiToken != "" {
		m.apiToken, m.hasToken = sha256.Sum256([]byte(apiToken)), true
	}
	m.Codes.OnRotateEarly = func() {
		log.Warn("nhập sai mã nhiều lần, đã đổi mã đăng nhập sớm")
	}
	return m
}

// Snapshot trả mã đăng nhập hiện hành và tình trạng khóa (cho lệnh login-code).
func (m *Manager) Snapshot() Current {
	c := m.Codes.Snapshot()
	c.LockedUntil = m.lim.lockedUntil()
	return c
}

// Unlock mở khóa đăng nhập, gỡ mọi khóa IP / thiết bị và xóa các lần sai đã đếm (lệnh `login-code -unlock`).
func (m *Manager) Unlock() {
	m.lim.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if n, err := m.Unblock(ctx, "", "", "login-code -unlock"); err != nil {
		m.log.Error("gỡ khóa đăng nhập trong DB thất bại", "err", err, "da_go_trong_ram", n)
	}
}

type ctxKey struct{}

// FromContext trả phiên của request (sau middleware Require).
func FromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(ctxKey{}).(Session)
	return s, ok
}

// Identify xác định người gọi: Bearer SCS_ADMIN_TOKEN (quyền Admin) hoặc cookie phiên.
func (m *Manager) Identify(r *http.Request) (Session, bool) {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && m.hasToken {
		got := sha256.Sum256([]byte(tok))
		if subtle.ConstantTimeCompare(got[:], m.apiToken[:]) == 1 {
			return Session{ID: "api-token", Name: "API token", Role: RoleAdmin, Level: LevelRoot}, true
		}
		return Session{}, false
	}
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Session{}, false
	}
	ip := clientIP(r)
	s, ok, stolen := m.sess.get(c.Value, ip, r.UserAgent())
	if stolen != nil {
		m.log.Warn("phiên đăng nhập bị dùng từ máy / trình duyệt khác lúc đăng nhập: đã thu hồi (có thể bị lộ cookie)",
			"name", stolen.Name, "session", stolen.ID, "ip_dang_nhap", stolen.IP, "ip_moi", ip,
			"trinh_duyet_khac", stolen.Agent != truncate(r.UserAgent(), 160))
	}
	return s, ok
}

// Require (cũ): RoleViewer = mọi người đã đăng nhập, RoleAdmin = quản trị gốc.
func (m *Manager) Require(need Role) func(http.Handler) http.Handler {
	if need == RoleAdmin {
		return m.RequireLevel(LevelRoot)
	}
	return m.RequireLevel(LevelView)
}

// RequireLevel chỉ cho qua phiên có mức quyền đủ (số mức <= need).
// Request thay đổi dữ liệu còn phải đến từ chính trang này (chống CSRF).
func (m *Manager) RequireLevel(need Level) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !safeMethod(r.Method) && !sameOrigin(r) {
				httpx.WriteError(w, http.StatusForbidden, "yêu cầu không đến từ trang quản trị")
				return
			}
			s, ok := m.Identify(r)
			if !ok {
				httpx.WriteError(w, http.StatusUnauthorized, "chưa đăng nhập hoặc phiên đã hết hạn")
				return
			}
			if !s.Level.Allows(need) {
				httpx.WriteError(w, http.StatusForbidden, "cần quyền: "+LevelNames[need])
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, s)))
		})
	}
}

// Mount gắn các route đăng nhập và quản lý phiên.
func (m *Manager) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/login", m.login)
	mux.HandleFunc("POST /auth/logout", m.logout)
	mux.HandleFunc("GET /auth/me", m.me)
	mux.Handle("GET /admin/v1/sessions", m.RequireLevel(LevelRoot)(http.HandlerFunc(m.listSessions)))
	mux.Handle("POST /admin/v1/sessions/{id}/revoke", m.RequireLevel(LevelRoot)(http.HandlerFunc(m.revokeSession)))
	mux.Handle("GET /admin/v1/login-blocks", m.RequireLevel(LevelCluster)(http.HandlerFunc(m.listBlocks)))
	mux.Handle("POST /admin/v1/login-blocks/remove", m.RequireLevel(LevelCluster)(http.HandlerFunc(m.removeBlock)))
	m.mountEmail(mux)
	m.mountTOTP(mux)
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, cookie string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: cookie, Path: "/",
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
		// Không đặt MaxAge/Expires: tắt trình duyệt là mất; server tự cắt sau 24 giờ.
	})
}

type loginReq struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func (m *Manager) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		httpx.WriteError(w, http.StatusForbidden, "yêu cầu không đến từ trang đăng nhập")
		return
	}
	who := m.subjectOf(r)
	if m.checkBlocked(w, r, who) {
		return
	}
	// Đặt chỗ lượt thử TRƯỚC khi đọc body: gửi nhiều request cùng lúc (giữ body lại)
	// cũng không vượt được giới hạn, vì lượt đã bị tính ngay từ đây.
	at := m.lim.begin(ip)
	if !at.ok {
		mins := int(at.wait.Minutes()) + 1
		httpx.WriteJSON(w, http.StatusTooManyRequests, map[string]any{
			"ok": false, "error": "thử sai quá nhiều, hãy thử lại sau " + itoa(mins) + " phút", "retry_after_s": int(at.wait.Seconds()),
		})
		return
	}
	if at.locked {
		m.log.Error("quá nhiều lần nhập sai mã trên toàn hệ thống, đã khóa đăng nhập web quản trị; "+
			"có thể đang bị dò mã. Mở khóa sớm trên máy server: scs-server login-code -unlock",
			"khoa_phut", int(at.lockFor.Minutes()), "ip", ip)
	}
	var req loginReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		m.lim.refund(ip) // chưa so mã: không tính
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	name, ok := cleanName(req.Name)
	if !ok {
		m.lim.refund(ip)
		httpx.WriteError(w, http.StatusBadRequest, "nhập tên của bạn (2–40 ký tự) để ghi vào nhật ký")
		return
	}
	code := strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, req.Code)

	role, ok := "", false
	if len(code) == 6 && isDigits(code) {
		var rl Role
		rl, ok = m.Codes.Use(code)
		role = string(rl)
	} else {
		m.Codes.Use("") // vẫn tính là một lần sai
	}
	if !ok {
		m.log.Warn("đăng nhập sai mã", "name", name, "ip", ip, "con_lai", at.left)
		m.codeFailed(who, rootSubject)
		httpx.WriteJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "mã không đúng, đã dùng hoặc đã hết hạn", "left": at.left})
		return
	}
	m.lim.refund(ip) // đúng mã: trả lại lượt đã đặt
	m.codeOK(who, rootSubject)

	cookie, s := m.sess.create(name, Role(role), ip, truncate(r.UserAgent(), 160))
	setSessionCookie(w, r, cookie)
	// "quyen" chứ không phải "role": logger đã có khóa "role" là vai trò tiến trình (admin/ingest).
	m.log.Info("đăng nhập web quản trị", "name", s.Name, "quyen", s.Role, "ip", ip, "session", s.ID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "name": s.Name, "role": s.Role, "expires": s.Expires})
}

func (m *Manager) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		httpx.WriteError(w, http.StatusForbidden, "yêu cầu không đến từ trang quản trị")
		return
	}
	if c, err := r.Cookie(CookieName); err == nil {
		if s, ok := m.sess.deleteCookie(c.Value); ok {
			m.log.Info("đăng xuất", "name", s.Name, "session", s.ID)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (m *Manager) me(w http.ResponseWriter, r *http.Request) {
	s, ok := m.Identify(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "chưa đăng nhập")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "id": s.ID, "name": s.Name, "role": s.Role,
		"level": s.Level, "level_name": LevelNames[s.Level], "account": s.Account, "expires": s.Expires})
}

func (m *Manager) listSessions(w http.ResponseWriter, r *http.Request) {
	me, _ := FromContext(r.Context())
	type item struct {
		Session
		Current bool `json:"current"`
	}
	list := m.sess.list()
	out := make([]item, len(list))
	for i, s := range list {
		out[i] = item{s, s.ID == me.ID}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": out})
}

func (m *Manager) revokeSession(w http.ResponseWriter, r *http.Request) {
	me, _ := FromContext(r.Context())
	s, ok := m.sess.revoke(r.PathValue("id"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "không tìm thấy phiên")
		return
	}
	m.log.Warn("thu hồi phiên đăng nhập", "by", me.Name, "name", s.Name, "session", s.ID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- tiện ích

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin: trình duyệt luôn gửi Origin với request POST; nếu có thì phải trùng
// với chính server. Script (curl, Bearer token) không gửi Origin thì cho qua.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return o == scheme+"://"+r.Host
}

func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// cleanName chuẩn hóa tên người đăng nhập (ghi vào log, hiện trong bảng phiên).
// Từ chối ký tự điều khiển và ký tự định dạng vô hình (Unicode Cf: đảo chiều chữ
// U+202E, khoảng trắng độ rộng 0...), vốn dùng được để làm tên hiển thị khác với
// tên thật, ví dụ U+202E đặt trước "nimdA" làm nó hiện thành "Admin". Dấu tiếng Việt (tổ hợp hay dựng sẵn)
// vẫn hợp lệ.
func cleanName(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	n := utf8.RuneCountInString(s)
	if n < 2 || n > 40 {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Co, unicode.Cs) || r == utf8.RuneError {
			return "", false
		}
	}
	return s, true
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

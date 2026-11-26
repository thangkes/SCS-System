package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// Đăng nhập bằng app xác thực (TOTP), không cần email / SMTP:
//
//	Đăng ký:  tên đăng nhập (email tùy chọn) → tài khoản "chờ duyệt".
//	Duyệt:    quản trị duyệt + đặt mức quyền, bấm "Tạo mã cài app" → mã cài đặt 8 số, hạn 15 phút, dùng một
//	          lần; quản trị gửi mã cho người dùng (Zalo / Teams...). Mã cài đặt KHÔNG cho vào hệ thống.
//	Cài app:  tên đăng nhập + mã cài đặt → trang hiện mã QR (một lần) → quét bằng app → nhập mã 6 số đầu
//	          tiên để xác nhận → lưu khóa bí mật (mã hóa) và đăng nhập luôn.
//	Đăng nhập: tên đăng nhập + mã 6 số đang hiện trong app.
//	Mất điện thoại: quản trị bấm "Đặt lại app xác thực" (xóa khóa, cắt phiên) rồi tạo mã cài đặt mới.
//
// Chống dò: dùng chung khóa IP / thiết bị (blocks.go), thêm giới hạn lần nhập theo tài khoản; mã đã dùng
// không dùng lại được (lưu bước thời gian cuối cùng trong DB).

const (
	setupCodeTTL       = 15 * time.Minute
	setupPendingTTL    = 10 * time.Minute
	totpAttemptsPerAcc = 10 // lần nhập mã app / tài khoản / 15 phút
	setupConfirmTries  = 5  // lần nhập mã xác nhận cho một mã QR
)

// TOTPStore: nơi lưu khóa TOTP của tài khoản (PostgreSQL ở internal/store).
type TOTPStore interface {
	// TOTPSecret trả khóa đã mã hóa (nil = chưa cài app) và bước thời gian đã dùng gần nhất.
	TOTPSecret(ctx context.Context, username string) (box []byte, lastStep int64, err error)
	// SetTOTP lưu khóa mới (thay khóa cũ), bước đã dùng = step; ghi nhật ký totp.setup.
	SetTOTP(ctx context.Context, username string, box []byte, step int64, by string) error
	// ClearTOTP xóa khóa (đặt lại app); ghi nhật ký totp.reset.
	ClearTOTP(ctx context.Context, username, by string) error
	// UseTOTPStep đánh dấu bước step đã dùng, chỉ khi step lớn hơn bước cũ (false = mã đã dùng rồi).
	UseTOTPStep(ctx context.Context, username string, step int64) (bool, error)
}

type pendingSetup struct {
	user   string
	secret []byte
	until  time.Time
	tries  int
}

type totpAuth struct {
	mu      sync.Mutex
	setup   map[string]*emailCode    // tên đăng nhập → mã cài đặt do quản trị tạo
	pending map[string]*pendingSetup // token trang QR → khóa đang chờ xác nhận
	perAcct *window
}

func newTOTPAuth() *totpAuth {
	return &totpAuth{setup: map[string]*emailCode{}, pending: map[string]*pendingSetup{},
		perAcct: newWindow(codeWindow, totpAttemptsPerAcc)}
}

// TOTPEnabled: có kho tài khoản, kho khóa và khóa mã hóa (SCS_TOTP_KEY).
func (m *Manager) TOTPEnabled() bool { return m.Accounts != nil && m.TOTPKeys != nil && m.TOTP != nil }

func (m *Manager) mountTOTP(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/totp/register", m.totpRegister)
	mux.HandleFunc("POST /auth/totp/setup", m.totpSetup)
	mux.HandleFunc("POST /auth/totp/confirm", m.totpConfirm)
	mux.HandleFunc("POST /auth/totp/login", m.totpLogin)
}

func (m *Manager) totpOff(w http.ResponseWriter) bool {
	if !m.TOTPEnabled() {
		httpx.WriteError(w, http.StatusServiceUnavailable, "server chưa bật đăng nhập bằng app xác thực (thiếu SCS_TOTP_KEY)")
		return true
	}
	return false
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (t *totpAuth) sweep(now time.Time) {
	for k, c := range t.setup {
		if !now.Before(c.until) {
			delete(t.setup, k)
		}
	}
	for k, p := range t.pending {
		if !now.Before(p.until) {
			delete(t.pending, k)
		}
	}
}

// totpRegister: POST /auth/totp/register {username, email?} — tạo tài khoản chờ duyệt không cần xác minh
// email (email nếu có chỉ để liên lạc, không dùng đăng nhập).
func (m *Manager) totpRegister(w http.ResponseWriter, r *http.Request) {
	if m.totpOff(w) {
		return
	}
	who := m.subjectOf(r)
	if m.checkBlocked(w, r, who) {
		return
	}
	var req accountReq
	if !jsonPost(w, r, &req) {
		return
	}
	user := normUsername(req.Username)
	if !registerNameRe.MatchString(user) {
		m.userMissed(who, "đăng ký tên sai định dạng")
		httpx.WriteError(w, http.StatusBadRequest, "tên đăng nhập chỉ gồm chữ số 0–9, từ 3 tới 32 số (không chữ cái, không ký tự đặc biệt)")
		return
	}
	ip, now := clientIP(r), time.Now()
	if !m.email.registers.take(ip, now) {
		httpx.WriteError(w, http.StatusTooManyRequests, "đăng ký quá nhiều lần từ máy này, thử lại sau")
		return
	}
	if n, err := m.Accounts.CountPending(r.Context()); err == nil && n >= maxPendingAccts {
		httpx.WriteError(w, http.StatusServiceUnavailable, "đang có quá nhiều tài khoản chờ duyệt, thử lại sau")
		return
	}
	reply := map[string]any{"ok": true, "message": "Nếu tên đăng nhập chưa được dùng, tài khoản đã được tạo và đang chờ quản trị duyệt. " +
		"Duyệt xong, quản trị gửi bạn mã cài đặt app xác thực (hạn 15 phút)."}
	err := m.Accounts.CreateAccount(r.Context(), Account{Username: user, Status: AccountPending})
	if errors.Is(err, ErrAccountExists) {
		httpx.WriteJSON(w, http.StatusOK, reply) // cùng câu trả lời: không lộ tên nào đã có
		return
	}
	if err != nil {
		m.log.Error("tạo tài khoản thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	m.log.Warn("tài khoản mới chờ duyệt (app xác thực)", "user", user, "ip", ip)
	httpx.WriteJSON(w, http.StatusOK, reply)
}

// issueSetupCode tạo mã cài đặt app xác thực cho tài khoản đang hoạt động (quản trị gọi, qua changeAccount).
func (m *Manager) issueSetupCode(user string) string {
	code := randomDigits(8)
	now := time.Now()
	m.totp.mu.Lock()
	m.totp.sweep(now)
	m.totp.setup[user] = &emailCode{hash: hashCode("setup\x00"+user, code), until: now.Add(setupCodeTTL)}
	m.totp.mu.Unlock()
	return code
}

// totpSetup: POST /auth/totp/setup {username, code} — đổi mã cài đặt lấy mã QR (một lần).
func (m *Manager) totpSetup(w http.ResponseWriter, r *http.Request) {
	if m.totpOff(w) {
		return
	}
	who := m.subjectOf(r)
	if m.checkBlocked(w, r, who) {
		return
	}
	var req accountReq
	if !jsonPost(w, r, &req) {
		return
	}
	user, ip, now := normUsername(req.Username), clientIP(r), time.Now()
	if !m.email.attempts.take(ip, now) {
		httpx.WriteError(w, http.StatusTooManyRequests, "nhập mã quá nhiều lần, thử lại sau")
		return
	}
	code := cleanCode(req.Code)
	m.totp.mu.Lock()
	c := m.totp.setup[user]
	delete(m.totp.setup, user) // mã cài đặt chỉ nhập được một lần, sai cũng hủy
	m.totp.mu.Unlock()
	ok := false
	if c != nil && now.Before(c.until) && len(code) == 8 {
		h := hashCode("setup\x00"+user, code)
		ok = constEq(h[:], c.hash[:])
	}
	if !ok {
		m.log.Warn("nhập mã cài đặt app xác thực sai", "user", user, "ip", ip)
		m.codeFailed(who, user)
		httpx.WriteError(w, http.StatusUnauthorized, "mã cài đặt không đúng, đã dùng hoặc đã hết hạn: nhờ quản trị tạo mã mới")
		return
	}
	a, err := m.Accounts.AccountByName(r.Context(), user)
	if err != nil || a.Status != AccountActive || !ValidAccountLevel(a.Level) {
		httpx.WriteError(w, http.StatusForbidden, "tài khoản không hoạt động")
		return
	}
	m.codeOK(who, user)
	secret := newTOTPKey()
	q, err := EncodeQR([]byte(totpURI(user, secret)))
	if err != nil {
		m.log.Error("không tạo được mã QR", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	token := randomToken()
	m.totp.mu.Lock()
	m.totp.sweep(now)
	m.totp.pending[token] = &pendingSetup{user: user, secret: secret, until: now.Add(setupPendingTTL)}
	m.totp.mu.Unlock()
	m.log.Info("mở trang cài app xác thực", "user", user, "ip", ip)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "token": token, "qr": q.SVGDataURL(6),
		"secret": groupSecret(secret), "account": totpIssuer + ":" + user, "expires_s": int(setupPendingTTL.Seconds())})
}

func constEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// totpConfirm: POST /auth/totp/confirm {token, code} — mã đầu tiên từ app đúng thì lưu khóa và đăng nhập.
func (m *Manager) totpConfirm(w http.ResponseWriter, r *http.Request) {
	if m.totpOff(w) {
		return
	}
	who := m.subjectOf(r)
	if m.checkBlocked(w, r, who) {
		return
	}
	var req struct {
		Token string `json:"token"`
		Code  string `json:"code"`
	}
	if !jsonPost(w, r, &req) {
		return
	}
	ip, now := clientIP(r), time.Now()
	if !m.email.attempts.take(ip, now) {
		httpx.WriteError(w, http.StatusTooManyRequests, "nhập mã quá nhiều lần, thử lại sau")
		return
	}
	m.totp.mu.Lock()
	p := m.totp.pending[req.Token]
	if p != nil && !now.Before(p.until) {
		delete(m.totp.pending, req.Token)
		p = nil
	}
	m.totp.mu.Unlock()
	if p == nil {
		httpx.WriteError(w, http.StatusGone, "trang cài đặt đã hết hạn: nhờ quản trị tạo mã cài đặt mới")
		return
	}
	step, ok := totpMatch(p.secret, cleanCode(req.Code), now, 0)
	if !ok {
		m.totp.mu.Lock()
		p.tries++
		if p.tries >= setupConfirmTries {
			delete(m.totp.pending, req.Token)
		}
		left := setupConfirmTries - p.tries
		m.totp.mu.Unlock()
		m.codeFailed(who, p.user)
		msg := "mã không đúng: kiểm giờ trên điện thoại (bật giờ tự động) rồi nhập mã đang hiện"
		if left <= 0 {
			msg = "nhập sai quá nhiều lần: nhờ quản trị tạo mã cài đặt mới"
		}
		httpx.WriteJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": msg, "left": max(left, 0)})
		return
	}
	m.totp.mu.Lock()
	delete(m.totp.pending, req.Token)
	m.totp.mu.Unlock()
	a, err := m.Accounts.AccountByName(r.Context(), p.user)
	if err != nil || a.Status != AccountActive || !ValidAccountLevel(a.Level) {
		httpx.WriteError(w, http.StatusForbidden, "tài khoản không còn hoạt động")
		return
	}
	if err := m.TOTPKeys.SetTOTP(r.Context(), p.user, m.TOTP.seal(p.user, p.secret), step, p.user); err != nil {
		m.log.Error("lưu khóa app xác thực thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	// Khóa mới thay khóa cũ: cắt phiên cũ (có thể của điện thoại đã mất).
	n := m.sess.revokeAccount(p.user)
	m.codeOK(who, p.user)
	m.log.Warn("đã cài app xác thực", "user", p.user, "ip", ip, "phien_cu_bi_cat", n)
	m.startAccountSession(w, r, a, "app xác thực (vừa cài)")
}

// totpLogin: POST /auth/totp/login {username, code}.
func (m *Manager) totpLogin(w http.ResponseWriter, r *http.Request) {
	if m.totpOff(w) {
		return
	}
	who := m.subjectOf(r)
	if m.checkBlocked(w, r, who) {
		return
	}
	var req accountReq
	if !jsonPost(w, r, &req) {
		return
	}
	user, ip, now := normUsername(req.Username), clientIP(r), time.Now()
	if !m.email.attempts.take(ip, now) || !m.totp.perAcct.take(user, now) {
		httpx.WriteError(w, http.StatusTooManyRequests, "nhập mã quá nhiều lần, thử lại sau 15 phút")
		return
	}
	fail := func(why string) {
		m.log.Warn("đăng nhập bằng app xác thực sai", "user", user, "ip", ip, "ly_do", why)
		m.codeFailed(who, user)
		httpx.WriteError(w, http.StatusUnauthorized, "mã không đúng hoặc đã dùng: nhập mã đang hiện trong app (mỗi mã dùng một lần)")
	}
	if !usernameRe.MatchString(user) {
		fail("tên sai định dạng")
		return
	}
	a, err := m.Accounts.AccountByName(r.Context(), user)
	if err != nil && !errors.Is(err, ErrNoAccount) {
		m.log.Error("đọc tài khoản thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	if err != nil || a.Status != AccountActive || !ValidAccountLevel(a.Level) {
		fail("tài khoản không có / không hoạt động")
		return
	}
	box, last, err := m.TOTPKeys.TOTPSecret(r.Context(), user)
	if err != nil {
		m.log.Error("đọc khóa app xác thực thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	if box == nil {
		fail("chưa cài app")
		return
	}
	secret, err := m.TOTP.open(user, box)
	if err != nil {
		m.log.Error("không giải mã được khóa app xác thực (SCS_TOTP_KEY đã đổi?)", "user", user, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "khóa app xác thực trên server không đọc được: nhờ quản trị đặt lại app")
		return
	}
	step, ok := totpMatch(secret, cleanCode(req.Code), now, last)
	if !ok {
		fail("mã sai / đã dùng")
		return
	}
	if fresh, err := m.TOTPKeys.UseTOTPStep(r.Context(), user, step); err != nil || !fresh {
		if err != nil {
			m.log.Error("ghi bước mã app thất bại", "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		fail("mã vừa được dùng ở request khác")
		return
	}
	m.codeOK(who, user)
	m.startAccountSession(w, r, a, "app xác thực")
}

// startAccountSession mở phiên với mức quyền của tài khoản (dùng chung cho email và app xác thực).
func (m *Manager) startAccountSession(w http.ResponseWriter, r *http.Request, a Account, how string) {
	ip := clientIP(r)
	_ = m.Accounts.TouchLogin(r.Context(), a.Username)
	ttl, maxSess := a.sessionLimits()
	cookie, s, evicted := m.sess.createLevel(a.Username, a.Username, a.Level, ip, truncate(r.UserAgent(), 160), ttl, maxSess)
	setSessionCookie(w, r, cookie)
	m.log.Info("đăng nhập web quản trị bằng "+how, "name", s.Name, "muc", int(s.Level), "ip", ip, "session", s.ID,
		"het_han", s.Expires.Format(time.DateTime), "phien_cu_bi_cat", evicted)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "name": s.Name, "level": s.Level, "expires": s.Expires})
}

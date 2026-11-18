package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// Đăng ký tài khoản và đăng nhập bằng mã gửi qua email (Gmail):
//
//	Đăng ký:   tên đăng nhập + email → mã xác minh gửi về email → nhập mã → tài khoản "chờ duyệt"
//	           → quản trị gốc duyệt và đặt mức quyền (1–7).
//	Đăng nhập: tên đăng nhập → mã gửi về email đã đăng ký → nhập mã → phiên với mức quyền của tài khoản.
//
// Mã 6 số, hạn 5 phút, CHỈ NHẬP ĐƯỢC MỘT LẦN (nhập sai là mã bị hủy, xin mã mới). Server luôn trả cùng
// một câu cho tên / email đúng hay sai, để người ngoài không dò được ai có tài khoản. Giới hạn số lần
// xin mã theo tài khoản và theo IP, để không làm ngập hộp thư / hết hạn mức gửi của Gmail.

const (
	emailCodeTTL     = 5 * time.Minute
	codeWindow       = 15 * time.Minute
	codesPerAccount  = 3  // lần xin mã / tài khoản / 15 phút
	codesPerIP       = 10 // lần xin mã / IP / 15 phút
	attemptsPerIP    = 20 // lần nhập mã / IP / 15 phút
	registersPerIP   = 5  // lần đăng ký / IP / giờ
	maxPendingAccts  = 50 // tài khoản chờ duyệt tối đa (chặn đăng ký rác)
	mailWorkers      = 4  // số mail gửi cùng lúc
	maxPendingCodes  = 5000
	genericCodeReply = "Nếu tài khoản tồn tại và đang hoạt động, mã đã được gửi tới email đã đăng ký (hạn 5 phút, nhập một lần)."
)

var (
	// usernameRe: tên đăng nhập hợp lệ khi đăng nhập / quản trị (giữ cho tài khoản cũ). registerNameRe: đăng
	// ký mới chỉ nhận chữ số 0–9.
	usernameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)
	registerNameRe = regexp.MustCompile(`^[0-9]{3,32}$`)
	emailRe        = regexp.MustCompile(`^[a-z0-9._%+-]{1,64}@[a-z0-9.-]{1,190}\.[a-z]{2,24}$`)
)

// emailCode: mã đang chờ nhập (lưu SHA-256, không lưu mã).
type emailCode struct {
	hash  [32]byte
	until time.Time
	email string // đăng ký: email chờ xác minh
}

// window: đếm số lần trong một cửa sổ thời gian theo khóa (IP, tài khoản).
type window struct {
	mu  sync.Mutex
	dur time.Duration
	max int
	m   map[string][]time.Time
}

func newWindow(dur time.Duration, max int) *window {
	return &window{dur: dur, max: max, m: map[string][]time.Time{}}
}

// take: còn lượt thì tính một lần và trả true.
func (w *window) take(key string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.m) > 20000 { // chặn phình RAM khi bị dò từ quá nhiều IP
		for k, v := range w.m {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > w.dur {
				delete(w.m, k)
			}
		}
	}
	list := w.m[key][:0]
	for _, t := range w.m[key] {
		if now.Sub(t) < w.dur {
			list = append(list, t)
		}
	}
	if len(list) >= w.max {
		w.m[key] = list
		return false
	}
	w.m[key] = append(list, now)
	return true
}

// emailAuth: phần đăng ký / đăng nhập qua email của Manager.
type emailAuth struct {
	mu        sync.Mutex
	login     map[string]*emailCode // tên đăng nhập → mã đăng nhập
	register  map[string]*emailCode // tên đăng nhập → mã xác minh đăng ký
	perAcct   *window
	perIP     *window
	attempts  *window
	registers *window
	sem       chan struct{}
}

func newEmailAuth() *emailAuth {
	return &emailAuth{login: map[string]*emailCode{}, register: map[string]*emailCode{},
		perAcct: newWindow(codeWindow, codesPerAccount), perIP: newWindow(codeWindow, codesPerIP),
		attempts: newWindow(codeWindow, attemptsPerIP), registers: newWindow(time.Hour, registersPerIP),
		sem: make(chan struct{}, mailWorkers)}
}

func randomDigits(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			panic(err)
		}
		b.WriteByte(byte('0' + d.Int64()))
	}
	return b.String()
}

func hashCode(username, code string) [32]byte {
	return sha256.Sum256([]byte("scs-email-code\x00" + username + "\x00" + code))
}

// EmailEnabled: có kho tài khoản và có cấu hình gửi mail.
func (m *Manager) EmailEnabled() bool { return m.Accounts != nil && m.Mail != nil }

// emailAllowed: email thuộc tên miền cho phép (rỗng = mọi tên miền).
func (m *Manager) emailAllowed(email string) bool {
	if len(m.EmailDomains) == 0 {
		return true
	}
	at := strings.LastIndexByte(email, '@')
	for _, d := range m.EmailDomains {
		if email[at+1:] == d {
			return true
		}
	}
	return false
}

// sendAsync gửi mail ngoài request (không để người gọi đo thời gian đoán tài khoản có tồn tại không).
func (m *Manager) sendAsync(to, subject, body, what string) {
	select {
	case m.email.sem <- struct{}{}:
	default:
		m.log.Warn("quá nhiều mail đang chờ gửi, bỏ qua", "viec", what)
		return
	}
	go func() {
		defer func() { <-m.email.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := m.Mail.Send(ctx, to, subject, body); err != nil {
			m.log.Error("không gửi được mail", "viec", what, "err", err)
		}
	}()
}

func (m *Manager) mountEmail(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/options", m.options)
	mux.HandleFunc("POST /auth/register", m.registerStart)
	mux.HandleFunc("POST /auth/register/verify", m.registerVerify)
	mux.HandleFunc("POST /auth/email/code", m.emailCodeReq)
	mux.HandleFunc("POST /auth/email/login", m.emailLogin)
	root := m.RequireLevel(LevelRoot)
	mux.Handle("GET /admin/v1/accounts", root(http.HandlerFunc(m.listAccounts)))
	mux.Handle("POST /admin/v1/accounts/{username}", root(http.HandlerFunc(m.changeAccount)))
}

// options: trang đăng nhập biết có đăng nhập / đăng ký bằng email không.
func (m *Manager) options(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "email": m.EmailEnabled(), "domains": m.EmailDomains})
}

func jsonPost(w http.ResponseWriter, r *http.Request, v any) bool {
	if !sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		httpx.WriteError(w, http.StatusForbidden, "yêu cầu không đến từ trang đăng nhập")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(v); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return false
	}
	return true
}

func (m *Manager) emailOff(w http.ResponseWriter) bool {
	if !m.EmailEnabled() {
		httpx.WriteError(w, http.StatusServiceUnavailable, "server chưa cấu hình gửi mail (SCS_SMTP_*): chưa đăng nhập / đăng ký bằng email được")
		return true
	}
	return false
}

type accountReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Code     string `json:"code"`
}

func normUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func cleanCode(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, s)
}

// registerStart: POST /auth/register {username, email} — gửi mã xác minh về email.
func (m *Manager) registerStart(w http.ResponseWriter, r *http.Request) {
	if m.emailOff(w) {
		return
	}
	var req accountReq
	if !jsonPost(w, r, &req) {
		return
	}
	user, email := normUsername(req.Username), strings.ToLower(strings.TrimSpace(req.Email))
	if !registerNameRe.MatchString(user) {
		httpx.WriteError(w, http.StatusBadRequest, "tên đăng nhập chỉ gồm chữ số 0–9, từ 3 tới 32 số (không chữ cái, không ký tự đặc biệt)")
		return
	}
	if !emailRe.MatchString(email) || !m.emailAllowed(email) {
		msg := "email không hợp lệ"
		if len(m.EmailDomains) > 0 {
			msg += " (chỉ nhận @" + strings.Join(m.EmailDomains, ", @") + ")"
		}
		httpx.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	ip, now := clientIP(r), time.Now()
	if !m.email.registers.take(ip, now) {
		httpx.WriteError(w, http.StatusTooManyRequests, "đăng ký quá nhiều lần từ máy này, thử lại sau")
		return
	}
	reply := map[string]any{"ok": true, "message": "Nếu tên đăng nhập và email chưa được dùng, mã xác minh đã được gửi tới email (hạn 5 phút, nhập một lần)."}
	if n, err := m.Accounts.CountPending(r.Context()); err == nil && n >= maxPendingAccts {
		httpx.WriteError(w, http.StatusServiceUnavailable, "đang có quá nhiều tài khoản chờ duyệt, thử lại sau")
		return
	}
	taken, err := m.Accounts.AccountTaken(r.Context(), user, email)
	if err != nil {
		m.log.Error("đọc tài khoản thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	if taken {
		httpx.WriteJSON(w, http.StatusOK, reply)
		return
	}
	code := randomDigits(6)
	m.email.mu.Lock()
	if len(m.email.register) < maxPendingCodes {
		m.email.register[user] = &emailCode{hash: hashCode(user, code), until: now.Add(emailCodeTTL), email: email}
	}
	m.email.mu.Unlock()
	m.sendAsync(email, "SCS-System: mã xác minh đăng ký "+code,
		"Mã xác minh đăng ký tài khoản \""+user+"\" trên web quản trị SCS-System:\n\n    "+code+
			"\n\nMã có hạn 5 phút và chỉ nhập được một lần. Nếu bạn không đăng ký, hãy bỏ qua email này.\n",
		"xác minh đăng ký")
	m.log.Info("yêu cầu đăng ký tài khoản", "user", user, "ip", ip)
	httpx.WriteJSON(w, http.StatusOK, reply)
}

// takeCode: lấy và XÓA mã đang chờ (mỗi mã chỉ nhập được một lần), so khớp.
func takeCode(mu *sync.Mutex, m map[string]*emailCode, user, code string, now time.Time) (*emailCode, bool) {
	mu.Lock()
	c := m[user]
	delete(m, user)
	mu.Unlock()
	if c == nil || !now.Before(c.until) || len(code) != 6 {
		return nil, false
	}
	h := hashCode(user, code)
	return c, subtle.ConstantTimeCompare(h[:], c.hash[:]) == 1
}

// registerVerify: POST /auth/register/verify {username, code} — tạo tài khoản chờ duyệt.
func (m *Manager) registerVerify(w http.ResponseWriter, r *http.Request) {
	if m.emailOff(w) {
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
	c, ok := takeCode(&m.email.mu, m.email.register, user, cleanCode(req.Code), now)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "mã không đúng hoặc đã hết hạn: mỗi mã chỉ nhập được một lần, hãy đăng ký lại")
		return
	}
	err := m.Accounts.CreateAccount(r.Context(), Account{Username: user, Email: c.email, Status: AccountPending})
	if errors.Is(err, ErrAccountExists) {
		httpx.WriteError(w, http.StatusConflict, "tên đăng nhập hoặc email vừa được người khác dùng")
		return
	}
	if err != nil {
		m.log.Error("tạo tài khoản thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	m.log.Warn("tài khoản mới chờ duyệt", "user", user, "ip", ip)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true,
		"message": "Đã xác minh email. Tài khoản đang chờ quản trị duyệt và đặt quyền; duyệt xong bạn đăng nhập bằng tên đăng nhập."})
}

// emailCodeReq: POST /auth/email/code {username} — gửi mã đăng nhập về email của tài khoản.
func (m *Manager) emailCodeReq(w http.ResponseWriter, r *http.Request) {
	if m.emailOff(w) {
		return
	}
	var req accountReq
	if !jsonPost(w, r, &req) {
		return
	}
	user, ip, now := normUsername(req.Username), clientIP(r), time.Now()
	reply := map[string]any{"ok": true, "message": genericCodeReply}
	if !usernameRe.MatchString(user) {
		httpx.WriteJSON(w, http.StatusOK, reply)
		return
	}
	if !m.email.perIP.take(ip, now) || !m.email.perAcct.take(user, now) {
		httpx.WriteError(w, http.StatusTooManyRequests, "xin mã quá nhiều lần, thử lại sau 15 phút")
		return
	}
	a, err := m.Accounts.AccountByName(r.Context(), user)
	if err != nil || a.Status != AccountActive || !ValidAccountLevel(a.Level) {
		if err != nil && !errors.Is(err, ErrNoAccount) {
			m.log.Error("đọc tài khoản thất bại", "err", err)
		}
		httpx.WriteJSON(w, http.StatusOK, reply)
		return
	}
	code := randomDigits(6)
	m.email.mu.Lock()
	if len(m.email.login) < maxPendingCodes {
		m.email.login[user] = &emailCode{hash: hashCode(user, code), until: now.Add(emailCodeTTL)}
	}
	m.email.mu.Unlock()
	m.sendAsync(a.Email, "SCS-System: mã đăng nhập "+code,
		"Mã đăng nhập web quản trị SCS-System cho tài khoản \""+user+"\":\n\n    "+code+
			"\n\nMã có hạn 5 phút và chỉ nhập được một lần. Yêu cầu từ IP "+ip+" lúc "+now.Format("15:04 02/01/2006")+
			".\nNếu không phải bạn, hãy báo quản trị: có người đang thử đăng nhập bằng tài khoản của bạn.\n",
		"mã đăng nhập")
	m.log.Info("gửi mã đăng nhập qua email", "user", user, "ip", ip)
	httpx.WriteJSON(w, http.StatusOK, reply)
}

// emailLogin: POST /auth/email/login {username, code}.
func (m *Manager) emailLogin(w http.ResponseWriter, r *http.Request) {
	if m.emailOff(w) {
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
	if _, ok := takeCode(&m.email.mu, m.email.login, user, cleanCode(req.Code), now); !ok {
		m.log.Warn("đăng nhập bằng mã email sai", "user", user, "ip", ip)
		httpx.WriteError(w, http.StatusUnauthorized, "mã không đúng hoặc đã hết hạn: mỗi mã chỉ nhập được một lần, hãy xin mã mới")
		return
	}
	a, err := m.Accounts.AccountByName(r.Context(), user)
	if err != nil || a.Status != AccountActive || !ValidAccountLevel(a.Level) {
		httpx.WriteError(w, http.StatusForbidden, "tài khoản không còn hoạt động")
		return
	}
	_ = m.Accounts.TouchLogin(r.Context(), user)
	ttl, maxSess := a.sessionLimits()
	cookie, s, evicted := m.sess.createLevel(user, user, a.Level, ip, truncate(r.UserAgent(), 160), ttl, maxSess)
	setSessionCookie(w, r, cookie)
	m.log.Info("đăng nhập web quản trị bằng email", "name", s.Name, "muc", int(s.Level), "ip", ip, "session", s.ID,
		"het_han", s.Expires.Format(time.DateTime), "phien_cu_bi_cat", evicted)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "name": s.Name, "level": s.Level, "expires": s.Expires})
}

// listAccounts: GET /admin/v1/accounts (chỉ quản trị gốc).
func (m *Manager) listAccounts(w http.ResponseWriter, r *http.Request) {
	if m.Accounts == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "accounts": []Account{}, "email": false})
		return
	}
	list, err := m.Accounts.ListAccounts(r.Context())
	if err != nil {
		m.log.Error("đọc tài khoản thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	if list == nil {
		list = []Account{}
	}
	names := map[int]string{}
	for l, n := range LevelNames {
		names[int(l)] = n
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "accounts": list, "levels": names, "email": m.EmailEnabled()})
}

// changeAccount: POST /admin/v1/accounts/{username} {action: approve|level|disable|enable|delete, level}.
func (m *Manager) changeAccount(w http.ResponseWriter, r *http.Request) {
	if m.Accounts == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "chưa có kho tài khoản")
		return
	}
	var req struct {
		Action       string `json:"action"`
		Level        Level  `json:"level"`
		SessionHours int    `json:"session_hours"`
		MaxSessions  int    `json:"max_sessions"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	user := normUsername(r.PathValue("username"))
	me, _ := FromContext(r.Context())
	a, err := m.Accounts.AccountByName(r.Context(), user)
	if errors.Is(err, ErrNoAccount) {
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	switch req.Action {
	case "approve", "level":
		if !ValidAccountLevel(req.Level) {
			httpx.WriteError(w, http.StatusBadRequest, "mức quyền phải từ 1 tới 7")
			return
		}
		if req.Action == "approve" && a.Status != AccountPending {
			httpx.WriteError(w, http.StatusConflict, "tài khoản không ở trạng thái chờ duyệt")
			return
		}
		status := a.Status
		if req.Action == "approve" {
			status = AccountActive
		}
		err = m.Accounts.UpdateAccount(r.Context(), user, status, req.Level, me.Name)
	case "disable":
		err = m.Accounts.UpdateAccount(r.Context(), user, AccountDisabled, a.Level, me.Name)
	case "enable":
		if !ValidAccountLevel(a.Level) {
			httpx.WriteError(w, http.StatusConflict, "tài khoản chưa có mức quyền: hãy duyệt")
			return
		}
		err = m.Accounts.UpdateAccount(r.Context(), user, AccountActive, a.Level, me.Name)
	case "delete":
		err = m.Accounts.DeleteAccount(r.Context(), user, me.Name)
	case "session":
		// Thời gian sống phiên và số phiên: áp ngay cho phiên đang mở (rút hạn, cắt phiên cũ nhất nếu thừa),
		// không cắt hết như đổi quyền.
		if !ValidSessionLimits(req.SessionHours, req.MaxSessions) {
			httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("thời gian sống phiên %d giờ – %d ngày, số phiên %d – %d",
				MinSessionHours, MaxSessionHours/24, MinMaxSessions, MaxMaxSessions))
			return
		}
		if err := m.Accounts.SetSessionLimits(r.Context(), user, req.SessionHours, req.MaxSessions, me.Name); err != nil {
			m.log.Error("đổi giới hạn phiên thất bại", "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		n := m.sess.applyLimits(user, time.Duration(req.SessionHours)*time.Hour, req.MaxSessions)
		m.log.Warn("quản trị gốc đổi giới hạn phiên", "user", user, "gio", req.SessionHours, "so_phien", req.MaxSessions,
			"by", me.Name, "phien_bi_cat", n)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions_cut": n})
		return
	default:
		httpx.WriteError(w, http.StatusBadRequest, "thao tác không hợp lệ")
		return
	}
	if err != nil {
		m.log.Error("đổi tài khoản thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	// Đổi quyền / khóa / xóa: cắt mọi phiên đang mở của tài khoản đó (đăng nhập lại để nhận quyền mới).
	n := m.sess.revokeAccount(user)
	m.log.Warn("quản trị gốc đổi tài khoản", "thao_tac", req.Action, "user", user, "muc", int(req.Level),
		"by", me.Name, "phien_bi_cat", n)
	if req.Action == "approve" && m.Mail != nil {
		m.sendAsync(a.Email, "SCS-System: tài khoản đã được duyệt",
			fmt.Sprintf("Tài khoản \"%s\" đã được duyệt với quyền: %s.\nĐăng nhập web quản trị bằng tên đăng nhập, mã sẽ được gửi về email này.\n",
				user, LevelNames[req.Level]), "báo duyệt tài khoản")
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

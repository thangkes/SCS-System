package auth

import (
	"context"
	"errors"
	"time"
)

// Level: mức quyền, số càng nhỏ quyền càng cao; mức trên có mọi quyền của mức dưới.
//
//	0 Quản trị gốc  — mã lấy bằng SSH trên máy server (scs-login-code) hoặc Bearer SCS_ADMIN_TOKEN;
//	                  duyệt tài khoản, đặt mức quyền, thu hồi phiên, và mọi việc bên dưới
//	1 Duyệt cụm server
//	2 Duyệt và phát hành rule
//	3 Duyệt agent, phát hành bản cập nhật agent
//	4 Địa chỉ server gửi cho agent
//	5 Xác nhận sự cố
//	6 Chỉnh thông tin agent (cấu hình, nhãn máy, nhóm thử)
//	7 Chỉ xem
type Level int

const (
	LevelRoot      Level = 0
	LevelCluster   Level = 1
	LevelRules     Level = 2
	LevelAgents    Level = 3
	LevelServers   Level = 4
	LevelIncidents Level = 5
	LevelAgentInfo Level = 6
	LevelView      Level = 7
)

// LevelNames: tên hiển thị từng mức (web, log).
var LevelNames = map[Level]string{
	LevelRoot:      "Quản trị gốc",
	LevelCluster:   "Duyệt cụm server",
	LevelRules:     "Duyệt và phát hành rule",
	LevelAgents:    "Duyệt agent, phát hành bản cập nhật",
	LevelServers:   "Địa chỉ server gửi cho agent",
	LevelIncidents: "Xác nhận sự cố",
	LevelAgentInfo: "Chỉnh thông tin agent",
	LevelView:      "Chỉ xem",
}

// ValidAccountLevel: mức gán được cho tài khoản (không có mức gốc).
func ValidAccountLevel(l Level) bool { return l >= LevelCluster && l <= LevelView }

// Allows: phiên ở mức l có làm được việc cần mức need không.
func (l Level) Allows(need Level) bool { return l <= need }

// Trạng thái tài khoản.
const (
	AccountPending  = "pending"  // đã xác minh email, chờ quản trị gốc duyệt
	AccountActive   = "active"   // đăng nhập được
	AccountDisabled = "disabled" // bị khóa
)

// Account: tài khoản đăng nhập bằng mã gửi qua email.
type Account struct {
	ID         int64     `json:"id"`
	Username   string    `json:"username"`
	Email      string    `json:"email"`
	Status     string    `json:"status"`
	Level      Level     `json:"level"` // 0 = chưa đặt (tài khoản chờ duyệt)
	CreatedAt  time.Time `json:"created_at"`
	ApprovedAt time.Time `json:"approved_at,omitzero"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	LastLogin  time.Time `json:"last_login,omitzero"`
	// SessionHours: thời gian sống của một phiên đăng nhập (giờ, tính từ lúc đăng nhập, không gia hạn).
	// MaxSessions: số phiên mở cùng lúc; đăng nhập thêm thì phiên cũ nhất bị cắt. Chỉ quản trị gốc đặt.
	SessionHours int `json:"session_hours"`
	MaxSessions  int `json:"max_sessions"`
	// TOTP: đã cài app xác thực (khóa bí mật không bao giờ trả ra ngoài).
	TOTP bool `json:"totp"`
}

// Giới hạn phiên của tài khoản (khớp CHECK trong migration 0013).
const (
	DefaultSessionHours = 24
	MinSessionHours     = 1
	MaxSessionHours     = 15 * 24
	DefaultMaxSessions  = 4
	MinMaxSessions      = 1
	MaxMaxSessions      = 20
)

// ValidSessionLimits: thời gian sống (giờ) và số phiên nằm trong giới hạn cho phép.
func ValidSessionLimits(hours, sessions int) bool {
	return hours >= MinSessionHours && hours <= MaxSessionHours && sessions >= MinMaxSessions && sessions <= MaxMaxSessions
}

// sessionLimits: giới hạn hiệu lực của tài khoản (giá trị 0 / ngoài khoảng → mặc định).
func (a Account) sessionLimits() (time.Duration, int) {
	h, n := a.SessionHours, a.MaxSessions
	if h < MinSessionHours || h > MaxSessionHours {
		h = DefaultSessionHours
	}
	if n < MinMaxSessions || n > MaxMaxSessions {
		n = DefaultMaxSessions
	}
	return time.Duration(h) * time.Hour, n
}

// Lỗi của kho tài khoản.
var (
	ErrNoAccount     = errors.New("không có tài khoản này")
	ErrAccountExists = errors.New("tên đăng nhập hoặc email đã được dùng")
)

// AccountStore: nơi lưu tài khoản (PostgreSQL ở internal/store; test dùng bản trong bộ nhớ).
type AccountStore interface {
	AccountByName(ctx context.Context, username string) (Account, error)
	AccountTaken(ctx context.Context, username, email string) (bool, error)
	CreateAccount(ctx context.Context, a Account) error // tạo ở trạng thái chờ duyệt
	ListAccounts(ctx context.Context) ([]Account, error)
	// UpdateAccount đổi trạng thái và mức quyền (by = người làm, ghi nhật ký thao tác).
	UpdateAccount(ctx context.Context, username, status string, level Level, by string) error
	DeleteAccount(ctx context.Context, username, by string) error
	TouchLogin(ctx context.Context, username string) error
	// SetSessionLimits đặt thời gian sống phiên (giờ) và số phiên tối đa (by = người làm, ghi nhật ký).
	SetSessionLimits(ctx context.Context, username string, hours, maxSessions int, by string) error
	CountPending(ctx context.Context) (int, error)
}

// Mailer gửi một email chữ thường.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

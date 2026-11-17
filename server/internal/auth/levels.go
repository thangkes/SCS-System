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
	CountPending(ctx context.Context) (int, error)
}

// Mailer gửi một email chữ thường.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"time"
)

// Registry là kho máy (PostgreSQL ở internal/store; test dùng bản giả trong bộ nhớ).
type Registry interface {
	// Enroll ghi nhận gói đăng ký: máy mới thì tạo yêu cầu chờ duyệt (kèm cảnh báo trên web),
	// máy đã có (cùng khóa công khai) thì cập nhật lần cuối thấy và trả trạng thái hiện tại.
	Enroll(ctx context.Context, r EnrollRequest) (EnrollResult, error)
	// SetCertSerial lưu serial chứng chỉ vừa cấp; chỉ serial mới nhất còn được nhận.
	SetCertSerial(ctx context.Context, deviceID int64, serial string) error

	ListDevices(ctx context.Context, status string) ([]Device, error)
	Approve(ctx context.Context, deviceID int64, by, storeCode string) (Device, error)
	Reject(ctx context.Context, deviceID int64, by string) error
	Revoke(ctx context.Context, deviceID int64, by string) error

	// ListAlerts: cảnh báo đang mở (includeAcked = false) hoặc cả đã xác nhận, mới nhất trước.
	ListAlerts(ctx context.Context, includeAcked bool) ([]Alert, error)
	// AckAlert: admin xác nhận đã xem cảnh báo (đóng cảnh báo).
	AckAlert(ctx context.Context, alertID int64, by string) error
}

// Alert: một cảnh báo, chỉ hiện trên web quản trị.
type Alert struct {
	ID          int64           `json:"id"`
	DeviceID    int64           `json:"device_id,omitempty"`
	Hostname    string          `json:"hostname,omitempty"`
	StoreCode   string          `json:"store_code,omitempty"`
	Kind        string          `json:"kind"`
	Severity    string          `json:"severity"`
	CreatedAt   time.Time       `json:"created_at"`
	LastAt      time.Time       `json:"last_at"`
	Occurrences int             `json:"occurrences"`
	Detail      json.RawMessage `json:"detail"`
	AckedAt     time.Time       `json:"acked_at,omitzero"`
	AckedBy     string          `json:"acked_by,omitempty"`
}

// Trạng thái máy trong kho.
const (
	DevicePending  = "pending"
	DeviceApproved = "approved"
	DeviceRejected = "rejected"
	DeviceRevoked  = "revoked"
)

// Lỗi nghiệp vụ của kho (handler đổi thành mã HTTP và câu báo dễ hiểu).
var (
	ErrNotFound       = errors.New("không tìm thấy máy")
	ErrWrongState     = errors.New("máy không ở trạng thái cho phép thao tác này")
	ErrNeedStore      = errors.New("hostname sai quy ước: phải nhập mã siêu thị khi duyệt")
	ErrConflict       = errors.New("trùng hostname hoặc vị trí TN với một máy đang hoạt động")
	ErrTooManyPending = errors.New("quá nhiều yêu cầu đang chờ duyệt")
	ErrInvalidStore   = errors.New("mã siêu thị chỉ gồm 1–10 chữ số")
)

// MaxPendingRequests: số yêu cầu chờ duyệt tối đa, chặn việc tạo hàng loạt yêu cầu giả làm phình DB.
const MaxPendingRequests = 5000

// EnrollRequest: gói đăng ký đã kiểm chữ ký, đã đọc hostname.
type EnrollRequest struct {
	PublicKey     []byte
	Fingerprint   string
	Components    json.RawMessage
	Hostname      string
	HostnameOK    bool
	HostnameError string // khi HostnameOK = false
	StoreCode     string // khi HostnameOK
	TN            int
	Role          string // "PRI", "SEC" hoặc ""
	OSVersion     string
	AgentVersion  string
	IP            netip.Addr
}

// EnrollResult: trạng thái hiện tại của máy sau khi ghi nhận.
type EnrollResult struct {
	DeviceID int64
	Status   string
	New      bool // vừa tạo yêu cầu mới
}

// Device: một máy, hiện trên web quản trị.
type Device struct {
	ID            int64     `json:"id"`
	Hostname      string    `json:"hostname"`
	HostnameOK    bool      `json:"hostname_ok"`
	HostnameError string    `json:"hostname_error,omitempty"`
	StoreCode     string    `json:"store_code,omitempty"`
	TN            int       `json:"tn,omitempty"`
	Role          string    `json:"role,omitempty"`
	Status        string    `json:"status"`
	KeyID         string    `json:"key_id"` // khớp "Mã khóa máy" lệnh scs-agent fingerprint in ra
	Fingerprint   string    `json:"fingerprint"`
	OSVersion     string    `json:"os_version"`
	AgentVersion  string    `json:"agent_version"`
	EnrollIP      string    `json:"enroll_ip"`
	StoreWANIP    string    `json:"store_wan_ip,omitempty"` // IP WAN cố định của siêu thị (nếu đã có)
	RequestedAt   time.Time `json:"requested_at"`
	DecidedAt     time.Time `json:"decided_at,omitzero"`
	DecidedBy     string    `json:"decided_by,omitempty"`
	LastSeen      time.Time `json:"last_seen,omitzero"`
	LastIP        string    `json:"last_ip,omitempty"`
}

// KeyID: 16 ký tự hex đầu của SHA-256 khóa công khai (giống identity.Key.ID phía agent).
func KeyID(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

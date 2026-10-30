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

	// AgentAuth: khóa công khai, trạng thái và serial chứng chỉ hiện hành của máy, để kiểm
	// gói tin của agent đã duyệt (heartbeat...). Không có máy: ErrNotFound.
	AgentAuth(ctx context.Context, deviceID int64) (AgentAuth, error)
	// Seen ghi lần cuối máy gọi về, IP và phiên bản agent.
	Seen(ctx context.Context, deviceID int64, ip netip.Addr, agentVersion string) error

	// Bản agent: thêm (đã có thì bỏ qua), liệt kê, lấy một bản.
	AddRelease(ctx context.Context, r Release) error
	ListReleases(ctx context.Context) ([]Release, error)
	GetRelease(ctx context.Context, version, arch string) (Release, error)
	// Phát hành: bản cho nhóm thử / mọi máy ("" = dừng phát hành nhóm đó). Bản phải có trong kho.
	GetRollout(ctx context.Context) (Rollout, error)
	SetRollout(ctx context.Context, ring, version, by string) error
	SetTestRing(ctx context.Context, deviceID int64, on bool, by string) error
	// AgentPlan: bản agent (theo nhóm của máy, đúng kiến trúc; nil = giữ bản đang chạy) và cấu hình
	// đã gộp (máy > siêu thị > chung) cho một máy.
	AgentPlan(ctx context.Context, deviceID int64, arch string) (AgentPlan, error)
	// Cấu hình agent theo từng cấp. SetConfig với WatchServices nil = bỏ ghi đè (kế thừa cấp trên).
	ListConfigs(ctx context.Context) ([]ConfigEntry, error)
	SetConfig(ctx context.Context, c ConfigEntry) error
}

// Release: một bản agent cho một kiến trúc, đã ký bằng khóa phát hành.
type Release struct {
	Version   string    `json:"version"`
	Arch      string    `json:"arch"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	Sig       string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// Rollout: bản đang phát hành cho nhóm thử và cho mọi máy ("" = chưa phát hành).
type Rollout struct {
	Test string `json:"test"`
	All  string `json:"all"`
}

// Nhóm phát hành.
const (
	RingTest = "test"
	RingAll  = "all"
)

// Cấp cấu hình agent.
const (
	ScopeGlobal = "global"
	ScopeStore  = "store"
	ScopeDevice = "device"
)

// ConfigEntry: cấu hình agent ở một cấp. WatchServices nil = kế thừa cấp trên.
type ConfigEntry struct {
	Scope         string    `json:"scope"`
	Key           string    `json:"key"`
	WatchServices []string  `json:"watch_services"`
	UpdatedAt     time.Time `json:"updated_at,omitzero"`
	UpdatedBy     string    `json:"updated_by,omitempty"`
}

// AgentPlan: những gì một máy cần chạy.
type AgentPlan struct {
	Release       *Release
	WatchServices []string
}

// AgentAuth: thông tin để xác thực gói tin của một máy.
type AgentAuth struct {
	PublicKey  []byte
	Status     string
	CertSerial string
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
	TestRing      bool      `json:"test_ring"` // thuộc nhóm thử: nhận bản agent mới trước
}

// KeyID: 16 ký tự hex đầu của SHA-256 khóa công khai (giống identity.Key.ID phía agent).
func KeyID(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

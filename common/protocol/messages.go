package protocol

import (
	"time"

	"github.com/thangkes/SCS-System/common/fingerprint"
)

// EnrollBody: gói đăng ký, được ký bằng chính khóa riêng vừa sinh (server kiểm bằng
// PublicKey trong body để chứng minh máy giữ khóa riêng). Không có bootstrap token:
// mọi yêu cầu đăng ký đều chờ admin duyệt. Máy đang chờ duyệt gửi lại gói này (cùng khóa)
// để hỏi trạng thái. Mã siêu thị và vị trí máy server đọc từ Hostname.
type EnrollBody struct {
	PublicKey  string                 `json:"public_key"`
	Components fingerprint.Components `json:"components"`
	Hostname   string                 `json:"hostname"`
	OSVersion  string                 `json:"os_version"`
}

// Trạng thái đăng ký trong EnrollResponse.
const (
	EnrollPending  = "pending"  // đang chờ admin duyệt, các trường khác trống
	EnrollApproved = "approved" // đã duyệt: có DeviceID và chứng chỉ
	EnrollRejected = "rejected" // admin từ chối: agent dừng gửi, chỉ ghi log cục bộ
)

// EnrollResponse: server trả về cho gói enroll.
type EnrollResponse struct {
	Status         string `json:"status"`
	DeviceID       string `json:"device_id,omitempty"`
	CertificatePEM string `json:"certificate_pem,omitempty"`
	CAPEM          string `json:"ca_pem,omitempty"`
	RuleSetVersion string `json:"rule_set_version,omitempty"`
	// Servers: các địa chỉ agent được dùng để gọi server (chỉ gửi khi đã duyệt), xem Response.Servers.
	Servers []string `json:"servers,omitempty"`
}

// HeartbeatBody: gửi mỗi 5 phút.
type HeartbeatBody struct {
	UptimeS         int64          `json:"uptime_s"`
	Arch            string         `json:"arch,omitempty"`           // amd64 | arm64: server chọn đúng bản agent
	ConfigVersion   string         `json:"config_version,omitempty"` // AgentConfig.Version agent đang dùng
	RuleSetVersion  string         `json:"rule_set_version"`
	LogQueuePending int            `json:"log_queue_pending"`
	Capabilities    []string       `json:"capabilities,omitempty"` // module agent đang có
	Status          *MachineStatus `json:"status,omitempty"`
}

// Giới hạn số phần tử trong MachineStatus để một heartbeat luôn nhỏ (server cũng kiểm).
const (
	MaxDisks    = 16
	MaxRAID     = 8
	MaxServices = 32
)

// MachineStatus: trạng thái máy agent đọc được ở mỗi heartbeat. Trường nào không đọc được
// thì để trống (zero), kèm lý do trong Errors, không làm hỏng cả heartbeat.
type MachineStatus struct {
	Hostname string    `json:"hostname"`
	OS       string    `json:"os"`     // ví dụ "Ubuntu 22.04.4 LTS"
	Kernel   string    `json:"kernel"` // uname -r
	CPUs     int       `json:"cpus"`
	Load     []float64 `json:"load"` // tải trung bình 1, 5, 15 phút

	MemTotalKB     uint64 `json:"mem_total_kb"`
	MemAvailableKB uint64 `json:"mem_available_kb"`
	SwapTotalKB    uint64 `json:"swap_total_kb"`
	SwapFreeKB     uint64 `json:"swap_free_kb"`

	Disks    []DiskStatus    `json:"disks,omitempty"`
	RAID     []RAIDStatus    `json:"raid,omitempty"`
	TimeSync TimeSyncStatus  `json:"time_sync"`
	Services []ServiceStatus `json:"services,omitempty"`
	Errors   []string        `json:"errors,omitempty"` // phần không đọc được và vì sao
}

// DiskStatus: một phân vùng đang mount (bỏ qua tmpfs, squashfs...).
type DiskStatus struct {
	Mount      string `json:"mount"`
	FS         string `json:"fs"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}

// RAIDStatus: một mảng RAID phần mềm (/proc/mdstat).
type RAIDStatus struct {
	Name     string `json:"name"`  // md0
	Level    string `json:"level"` // raid1
	Disks    int    `json:"disks"` // số ổ của mảng
	Active   int    `json:"active"`
	Degraded bool   `json:"degraded"`
	Sync     string `json:"sync,omitempty"` // "recovery 42.5%" khi đang đồng bộ lại
}

// TimeSyncStatus: đồng hồ có được đồng bộ NTP không (timedatectl).
type TimeSyncStatus struct {
	Synced  bool `json:"synced"`
	NTPOn   bool `json:"ntp_on"`
	Checked bool `json:"checked"` // false: không đọc được
}

// ServiceStatus: một dịch vụ systemd agent được cấu hình theo dõi (ví dụ dịch vụ POS).
type ServiceStatus struct {
	Name   string `json:"name"`
	Active string `json:"active"` // active | inactive | failed | activating | unknown
	Sub    string `json:"sub,omitempty"`
}

// LogBatchBody: lô sự kiện khớp rule.
type LogBatchBody struct {
	Events   []LogEvent     `json:"events"`
	Counters map[string]int `json:"counters,omitempty"`
}

type LogEvent struct {
	RuleID   string    `json:"rule_id"`
	Severity string    `json:"severity"` // low | medium | high | critical
	Source   string    `json:"source"`
	Time     time.Time `json:"time"`
	Line     string    `json:"line"`
	Count    int       `json:"count"`
}

// Response: phản hồi chung của server cho heartbeat và log_batch. Server không gửi lệnh:
// chỉ báo đã nhận, nhịp gọi tiếp, và phiên bản rule / bản cập nhật để agent tự kéo về.
type Response struct {
	OK             bool         `json:"ok"`
	NextPollS      int          `json:"next_poll_s,omitempty"`
	RuleSetVersion string       `json:"rule_set_version,omitempty"`
	Update         *UpdateInfo  `json:"update,omitempty"`
	Config         *AgentConfig `json:"config,omitempty"`
	ServerTime     time.Time    `json:"server_time"` // để agent bù khi đồng hồ lệch
	Error          string       `json:"error,omitempty"`
	// Servers: danh sách địa chỉ server (LAN, IP WAN, tên miền...) theo thứ tự ưu tiên. Agent lưu
	// lại và thử lần lượt khi địa chỉ đang dùng không kết nối được. Rỗng = giữ danh sách đang có.
	Servers []string `json:"servers,omitempty"`
}

// UpdateInfo: bản agent máy này cần chạy (khác bản đang chạy thì agent tải về). Sig là chữ ký
// Ed25519 (base64) bằng khóa phát hành của server trên ReleaseSigningInput; agent kiểm bằng khóa
// công khai đóng sẵn trong gói cài trước khi chạy bản mới.
type UpdateInfo struct {
	Version string `json:"version"`
	Arch    string `json:"arch"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Sig     string `json:"sig"`
}

// AgentConfig: cấu hình nghiệp vụ server gửi xuống (đặt trên web: chung, theo siêu thị, theo máy).
// Version là mã băm nội dung: agent gửi lại trong heartbeat, server chỉ gửi khi khác.
type AgentConfig struct {
	Version       string   `json:"version"`
	WatchServices []string `json:"watch_services"`
}

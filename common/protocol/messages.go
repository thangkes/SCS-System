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
	UptimeS         int64    `json:"uptime_s"`
	Arch            string   `json:"arch,omitempty"`           // amd64 | arm64: server chọn đúng bản agent
	ConfigVersion   string   `json:"config_version,omitempty"` // AgentConfig.Version agent đang dùng
	RuleSetVersion  string   `json:"rule_set_version"`
	LogQueuePending int      `json:"log_queue_pending"`
	Capabilities    []string `json:"capabilities,omitempty"` // module agent đang có
	// ServersHash: ServersHash của danh sách địa chỉ server agent đang giữ; ServerURL: địa chỉ
	// agent vừa dùng để gọi heartbeat này. Server dùng hai trường để biết đã bao nhiêu máy nhận
	// danh sách mới và còn bao nhiêu máy gọi qua từng địa chỉ (trước khi gỡ một địa chỉ cũ).
	ServersHash string         `json:"servers_hash,omitempty"`
	ServerURL   string         `json:"server_url,omitempty"`
	Status      *MachineStatus `json:"status,omitempty"`
	// Rules: bộ rule đang cài trên máy (nil = agent bản cũ, không báo).
	Rules *RuleSetReport `json:"rules,omitempty"`
	// UpdateError: lần tải / kiểm bản agent gần nhất lỗi (nil = không lỗi). Server đếm để hiện trên web.
	UpdateError *UpdateError `json:"update_error,omitempty"`
	// LAN: kết quả lượt kiểm thiết bị LAN gần nhất (sơ đồ LAN của bộ rule, schema 4); chỉ có ở heartbeat ngay
	// sau một lượt kiểm, gửi lại tới khi server nhận. nil = không có lượt mới.
	LAN *LANReport `json:"lan,omitempty"`
}

// Trạng thái một lượt kiểm thiết bị LAN.
const (
	LANStateOK          = "ok"          // đã kiểm
	LANStateProbesOff   = "probes_off"  // máy tắt kiểm thiết bị (SCS_PROBES=off)
	LANStateICMPDenied  = "icmp_denied" // máy không cho mở socket ping (net.ipv4.ping_group_range)
	LANStateNoLAN       = "no_lan"      // không card mạng nào cùng dải với sơ đồ
	LANStateUnsupported = "unsupported" // hệ điều hành không hỗ trợ
)

// MaxLANBits: số bit tối đa của bitmap LAN (≥ rules.MaxLANTargets).
const MaxLANBits = 64

// LANReport: kết quả một lượt kiểm sơ đồ LAN. Bit i của Up / Skip ứng với đích thứ i của sơ đồ có dấu ListV.
// Up, Skip: số nguyên 64 bit viết hex (16 ký tự). Skip: đích máy không kiểm (ngoài dải mạng của máy, kiểu
// kiểm không làm được). Thời điểm do server ghi lúc nhận (At chỉ để tham khảo).
type LANReport struct {
	ListV string    `json:"list_v"`
	N     int       `json:"n"` // số đích trong sơ đồ
	Up    string    `json:"up"`
	Skip  string    `json:"skip,omitempty"`
	State string    `json:"state"`
	At    time.Time `json:"at"`
	DurMs int       `json:"dur_ms,omitempty"`
	LANIP string    `json:"lan_ip,omitempty"` // IPv4 của máy trong dải mạng của sơ đồ
	Round int       `json:"round,omitempty"`  // số thứ tự lượt kiểm từ khi agent chạy
}

// UpdateError: agent không cài được bản agent server yêu cầu.
type UpdateError struct {
	Version string `json:"version"`
	Error   string `json:"error"` // ≤ 200 ký tự
}

// RuleSetReport: agent báo bộ rule đang cài để web biết máy nào đã áp dụng bộ nào.
type RuleSetReport struct {
	Installed bool   `json:"installed"`          // có bộ rule đã cài và kiểm chữ ký đạt
	Name      string `json:"name,omitempty"`     // tên bộ
	Version   string `json:"version,omitempty"`  // phiên bản
	SHA256    string `json:"sha256,omitempty"`   // phần đã ký (khớp bản ký trong kho rule của server)
	Total     int    `json:"total,omitempty"`    // rule đang bật trong bộ
	Active    int    `json:"active,omitempty"`   // rule chạy trên máy này (khớp applies_to)
	Scanning  bool   `json:"scanning,omitempty"` // agent đang quét log liên tục theo bộ này (mốc A3)
	Error     string `json:"error,omitempty"`    // bộ đang cài hỏng / chữ ký sai (≤ 200 ký tự)
}

// Giới hạn số phần tử trong MachineStatus để một heartbeat luôn nhỏ (server cũng kiểm).
const (
	MaxDisks    = 16
	MaxRAID     = 8
	MaxServices = 32
	MaxBlock    = 16 // ổ vật lý
	MaxErrors   = 16
	MaxSmart    = 16 // ổ đọc SMART
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

	BootUptimeS int64 `json:"boot_uptime_s,omitempty"` // máy đã chạy bao lâu (giây) — khác uptime của agent
	TempC       int   `json:"temp_c,omitempty"`        // nhiệt độ cao nhất các cảm biến (°C); 0 = không đọc được

	Disks    []DiskStatus    `json:"disks,omitempty"`
	Block    []BlockDevice   `json:"block,omitempty"` // ổ vật lý (/sys/block)
	RAID     []RAIDStatus    `json:"raid,omitempty"`
	TimeSync TimeSyncStatus  `json:"time_sync"`
	Services []ServiceStatus `json:"services,omitempty"`
	Errors   []string        `json:"errors,omitempty"` // phần không đọc được và vì sao
	// Smart: kết quả đọc SMART gần nhất (tác vụ root scs-agent-smart.timer ghi file, agent chỉ đọc); nil = agent bản cũ.
	Smart *SmartReport `json:"smart,omitempty"`
}

// SmartReport: kết quả một lần đọc SMART các ổ vật lý.
type SmartReport struct {
	At    time.Time   `json:"at,omitzero"`     // lúc đọc (zero = chưa có lần đọc nào)
	Disks []SmartDisk `json:"disks,omitempty"` //
	Error string      `json:"error,omitempty"` // cả lần đọc không làm được (chưa cài tác vụ, file hỏng…)
}

// SmartDisk: SMART của một ổ. Con trỏ nil = ổ không có thông số đó.
type SmartDisk struct {
	Name   string `json:"name"`           // sda, nvme0n1
	Kind   string `json:"kind"`           // ata | nvme
	OK     bool   `json:"ok"`             // đọc được SMART
	Note   string `json:"note,omitempty"` // không đọc được vì sao (sau card RAID, ổ USB, ổ SAS…)
	Passed *bool  `json:"passed,omitempty"`
	// Passed: ổ tự đánh giá — NVMe: không có cờ cảnh báo nghiêm trọng; ATA: không thuộc tính "pre-fail" nào chạm ngưỡng.
	TempC        int    `json:"temp_c,omitempty"`
	PowerOnHours *int64 `json:"power_on_hours,omitempty"`
	PowerCycles  *int64 `json:"power_cycles,omitempty"`
	// PercentUsed: % tuổi thọ đã dùng. NVMe: trường chuẩn. SSD SATA: suy từ thuộc tính hãng (WearAttr), chỉ ước tính.
	PercentUsed *int `json:"percent_used,omitempty"`
	WearAttr    int  `json:"wear_attr,omitempty"`
	// NVMe
	CriticalWarning int    `json:"critical_warning,omitempty"`
	AvailSpare      *int   `json:"avail_spare,omitempty"`
	MediaErrors     *int64 `json:"media_errors,omitempty"`
	UnsafeShutdowns *int64 `json:"unsafe_shutdowns,omitempty"`
	// ATA (giá trị thô)
	Reallocated   *int64   `json:"reallocated,omitempty"`        // 5: sector đã thay
	Pending       *int64   `json:"pending,omitempty"`            // 197: sector chờ thay
	Uncorrectable *int64   `json:"uncorrectable,omitempty"`      // 198: sector không đọc được khi tự kiểm
	ReportedUncor *int64   `json:"reported_uncorrect,omitempty"` // 187
	CRCErrors     *int64   `json:"crc_errors,omitempty"`         // 199: lỗi đường truyền (cáp / cổng SATA)
	Failing       []string `json:"failing,omitempty"`            // thuộc tính đang dưới ngưỡng
}

// DiskStatus: một phân vùng đang mount (bỏ qua tmpfs, squashfs...).
type DiskStatus struct {
	Mount       string `json:"mount"`
	FS          string `json:"fs"`
	Device      string `json:"device,omitempty"` // /dev/sda2
	TotalBytes  uint64 `json:"total_bytes"`
	FreeBytes   uint64 `json:"free_bytes"`
	InodesTotal uint64 `json:"inodes_total,omitempty"`
	InodesFree  uint64 `json:"inodes_free,omitempty"`
	ReadOnly    bool   `json:"read_only,omitempty"` // đang mount chỉ đọc (ổ lỗi nặng thường bị kernel chuyển sang ro)
}

// BlockDevice: một ổ vật lý (/sys/block, bỏ loop / ram / zram / dm / md / ổ rời).
type BlockDevice struct {
	Name      string `json:"name"` // sda, nvme0n1
	Model     string `json:"model,omitempty"`
	SizeBytes uint64 `json:"size_bytes"`
	SSD       bool   `json:"ssd"` // không quay (queue/rotational = 0)
	// IOErrors: số lần lỗi I/O kernel đếm cho ổ từ lúc khởi động (device/ioerr_cnt, ổ SATA/SAS); IOErrKnown =
	// false khi ổ không có bộ đếm này (NVMe, ổ ảo).
	IOErrors   int64 `json:"io_errors"`
	IOErrKnown bool  `json:"io_err_known"`
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
	OK        bool `json:"ok"`
	NextPollS int  `json:"next_poll_s,omitempty"`
	// NextPollMaxS: agent chờ ngẫu nhiên (theo giây) từ NextPollS tới NextPollMaxS rồi mới gọi tiếp.
	// 0 = agent tự tính (NextPollS × SpreadMax của agent). Server đổi được khoảng mà không cần bản agent mới.
	NextPollMaxS   int          `json:"next_poll_max_s,omitempty"`
	RuleSetVersion string       `json:"rule_set_version,omitempty"`
	Update         *UpdateInfo  `json:"update,omitempty"`
	Config         *AgentConfig `json:"config,omitempty"`
	ServerTime     time.Time    `json:"server_time"` // để agent bù khi đồng hồ lệch
	Error          string       `json:"error,omitempty"`
	// Servers: danh sách địa chỉ server (LAN, IP WAN, tên miền...) theo thứ tự ưu tiên. Agent lưu
	// lại và thử lần lượt khi địa chỉ đang dùng không kết nối được. Rỗng = giữ danh sách đang có.
	Servers []string `json:"servers,omitempty"`
	// AlertSeverity: mức cảnh báo đang mở của máy trên server (high | critical; rỗng = máy đang OK). Agent chỉ
	// gửi gấp sự kiện nặng HƠN mức này (chuyển trạng thái); sự kiện cùng mức gom theo nhịp thường. Server cũ
	// không gửi trường này → agent coi như OK và gửi gấp như trước.
	AlertSeverity string `json:"alert_severity,omitempty"`
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
	Version string `json:"version"`
	// WatchServices: null = server không đặt (agent dùng agent.env).
	WatchServices []string `json:"watch_services"`
	// Labels: nhãn ngữ cảnh máy (role, store, os, khóa tự do) ghi đè giá trị agent tự suy ra; dùng
	// chọn rule theo applies_to. Gộp theo khóa: chung < siêu thị < máy.
	Labels map[string]string `json:"labels,omitempty"`
}

// CapRulesPrefix: agent báo "rules/<schema cao nhất đọc được>" trong HeartbeatBody.Capabilities.
const CapRulesPrefix = "rules/"

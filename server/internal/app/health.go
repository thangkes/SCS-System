package app

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/common/protocol"
)

// Sức khỏe máy tính từ trạng thái agent gửi trong heartbeat (mốc A2).
//
// Mỗi vấn đề có mã (ví dụ "disk_full:/var") và mức. Máy: ok (không có vấn đề), warn (chỉ mức medium), bad
// (có high / critical). Vấn đề MỚI xuất hiện (hoặc tăng mức) được ghi thành sự kiện "may.<mã>" vào giai
// đoạn của máy — mở cảnh báo, hiện thông báo trên web như sự kiện log. Trạng thái hiện tại (vấn đề đang còn)
// luôn hiện ở trang Máy; hết vấn đề thì tự xóa khỏi trạng thái.

// HealthPrefix: tiền tố mã rule của sự kiện sức khỏe máy trong giai đoạn / cảnh báo.
const HealthPrefix = "may."

// Ngưỡng (đổi được khi test).
var (
	HealthDiskHigh     = 90.0 // % đã dùng
	HealthDiskCritical = 95.0
	HealthInodeHigh    = 95.0
	HealthMemLowPct    = 5.0 // RAM còn dưới bấy nhiêu %…
	HealthMemLowStreak = 3   // …liên tiếp bấy nhiêu heartbeat
	HealthTempHigh     = 90  // °C
)

// HealthIssue: một vấn đề sức khỏe đang có.
type HealthIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // medium | high | critical
	Text     string `json:"text"`
}

// Health: sức khỏe máy theo heartbeat gần nhất.
type Health struct {
	Level  string        `json:"level"` // ok | warn | bad
	Issues []HealthIssue `json:"issues"`
}

var rwFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "f2fs": true, "zfs": true}

// memStreak đếm số heartbeat liên tiếp RAM thấp của từng máy (trong RAM; khởi động lại server thì đếm lại).
var memStreak = struct {
	sync.Mutex
	m map[int64]int
}{m: map[int64]int{}}

func lowMemStreak(id int64, low bool) int {
	memStreak.Lock()
	defer memStreak.Unlock()
	if !low {
		delete(memStreak.m, id)
		return 0
	}
	if len(memStreak.m) > 100000 {
		memStreak.m = map[int64]int{}
	}
	memStreak.m[id]++
	return memStreak.m[id]
}

func usedPct(total, free uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(total-min(free, total)) * 100 / float64(total)
}

// evalHealth đánh giá trạng thái máy. memLow: số heartbeat liên tiếp RAM thấp (kể cả lần này).
func evalHealth(st *protocol.MachineStatus, memLow int) Health {
	h := Health{Level: "ok", Issues: []HealthIssue{}}
	if st == nil {
		return h
	}
	add := func(code, sev, format string, a ...any) {
		h.Issues = append(h.Issues, HealthIssue{Code: code, Severity: sev, Text: fmt.Sprintf(format, a...)})
	}
	for _, d := range st.Disks {
		switch p := usedPct(d.TotalBytes, d.FreeBytes); {
		case p >= HealthDiskCritical:
			add("disk_full:"+d.Mount, "critical", "Phân vùng %s đầy %.0f%%", d.Mount, p)
		case p >= HealthDiskHigh:
			add("disk_full:"+d.Mount, "high", "Phân vùng %s đầy %.0f%%", d.Mount, p)
		}
		if d.InodesTotal > 0 {
			if p := usedPct(d.InodesTotal, d.InodesFree); p >= HealthInodeHigh {
				add("inode_full:"+d.Mount, "high", "Phân vùng %s hết inode (%.0f%%): còn dung lượng nhưng không tạo được file mới", d.Mount, p)
			}
		}
		if d.ReadOnly && rwFS[d.FS] {
			add("fs_ro:"+d.Mount, "critical", "Phân vùng %s (%s) đang chỉ đọc: thường do ổ lỗi, không ghi được dữ liệu", d.Mount, d.FS)
		}
	}
	for _, b := range st.Block {
		if b.IOErrKnown && b.IOErrors > 0 {
			add("io_err:"+b.Name, "high", "Ổ %s%s có %d lỗi I/O từ lần khởi động", b.Name, modelSuffix(b.Model), b.IOErrors)
		}
	}
	for _, r := range st.RAID {
		if r.Degraded {
			add("raid:"+r.Name, "critical", "RAID %s (%s) hỏng: %d/%d ổ hoạt động", r.Name, r.Level, r.Active, r.Disks)
		}
	}
	for _, s := range st.Services {
		if s.Active == "failed" || s.Active == "inactive" {
			add("svc:"+s.Name, "high", "Dịch vụ %s không chạy (%s)", s.Name, s.Active)
		}
	}
	if st.TimeSync.Checked && !st.TimeSync.Synced {
		add("time", "medium", "Đồng hồ máy chưa đồng bộ NTP")
	}
	if memLow >= HealthMemLowStreak && st.MemTotalKB > 0 {
		add("mem", "medium", "RAM còn %.0f%% (%d lần gửi liên tiếp)", float64(st.MemAvailableKB)*100/float64(st.MemTotalKB), memLow)
	}
	if st.TempC >= HealthTempHigh {
		add("temp", "high", "Nhiệt độ máy %d°C", st.TempC)
	}
	sort.SliceStable(h.Issues, func(i, j int) bool { return sevRank(h.Issues[i].Severity) > sevRank(h.Issues[j].Severity) })
	for _, is := range h.Issues {
		if sevRank(is.Severity) >= sevRank("high") {
			h.Level = "bad"
			break
		}
		h.Level = "warn"
	}
	return h
}

func memIsLow(st *protocol.MachineStatus) bool {
	return st != nil && st.MemTotalKB > 0 && float64(st.MemAvailableKB)*100/float64(st.MemTotalKB) < HealthMemLowPct
}

func modelSuffix(m string) string {
	if m == "" {
		return ""
	}
	return " (" + m + ")"
}

func sevRank(s string) int {
	switch s {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}

// NewHealthIssues: vấn đề có trong cur mà prev chưa có, hoặc tăng mức — ghi thành sự kiện.
func NewHealthIssues(prev, cur []HealthIssue) []HealthIssue {
	old := map[string]int{}
	for _, p := range prev {
		old[p.Code] = sevRank(p.Severity)
	}
	var out []HealthIssue
	for _, c := range cur {
		if r, ok := old[c.Code]; !ok || sevRank(c.Severity) > r {
			out = append(out, c)
		}
	}
	return out
}

// HealthTitle: tiêu đề hiển thị của sự kiện sức khỏe "may.<mã>" (trang Cảnh báo, dòng thời gian).
func HealthTitle(rule string) string {
	code, ok := strings.CutPrefix(rule, HealthPrefix)
	if !ok {
		return ""
	}
	kind, arg, _ := strings.Cut(code, ":")
	switch kind {
	case "disk_full":
		return "Phân vùng " + arg + " sắp đầy"
	case "inode_full":
		return "Phân vùng " + arg + " hết inode"
	case "fs_ro":
		return "Phân vùng " + arg + " bị chuyển sang chỉ đọc"
	case "io_err":
		return "Ổ " + arg + " có lỗi I/O"
	case "raid":
		return "RAID " + arg + " hỏng"
	case "svc":
		return "Dịch vụ " + arg + " không chạy"
	case "time":
		return "Đồng hồ chưa đồng bộ"
	case "mem":
		return "RAM thấp kéo dài"
	case "temp":
		return "Nhiệt độ máy cao"
	}
	return "Sức khỏe máy"
}

// cleanStatus giới hạn và làm sạch trạng thái máy agent gửi (không tin agent): số phần tử, độ dài chuỗi.
func cleanStatus(st *protocol.MachineStatus) *protocol.MachineStatus {
	if st == nil {
		return nil
	}
	c := *st
	str := func(s string, n int) string {
		s = cleanLine(s)
		for len(s) > n {
			s = s[:n]
			for !utf8.ValidString(s) {
				s = s[:len(s)-1]
			}
		}
		return s
	}
	c.Hostname, c.OS, c.Kernel = str(c.Hostname, 64), str(c.OS, 128), str(c.Kernel, 128)
	if len(c.Load) > 3 {
		c.Load = c.Load[:3]
	}
	c.CPUs = min(max(c.CPUs, 0), 4096)
	c.TempC = min(max(c.TempC, 0), 200)
	c.BootUptimeS = max(c.BootUptimeS, 0)
	c.Disks = clipList(c.Disks, protocol.MaxDisks)
	for i := range c.Disks {
		d := &c.Disks[i]
		d.Mount, d.FS, d.Device = str(d.Mount, 128), str(d.FS, 16), str(d.Device, 128)
	}
	c.Block = clipList(c.Block, protocol.MaxBlock)
	for i := range c.Block {
		b := &c.Block[i]
		b.Name, b.Model = str(b.Name, 32), str(b.Model, 64)
		b.IOErrors = max(b.IOErrors, 0)
	}
	c.RAID = clipList(c.RAID, protocol.MaxRAID)
	for i := range c.RAID {
		r := &c.RAID[i]
		r.Name, r.Level, r.Sync = str(r.Name, 32), str(r.Level, 16), str(r.Sync, 64)
	}
	c.Services = clipList(c.Services, protocol.MaxServices)
	for i := range c.Services {
		s := &c.Services[i]
		s.Name, s.Active, s.Sub = str(s.Name, 128), str(s.Active, 16), str(s.Sub, 32)
	}
	c.Errors = clipList(c.Errors, protocol.MaxErrors)
	for i := range c.Errors {
		c.Errors[i] = str(c.Errors[i], 200)
	}
	return &c
}

func clipList[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

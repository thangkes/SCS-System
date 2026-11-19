package app

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/common/protocol"
)

// Sức khỏe máy tính từ trạng thái agent gửi trong heartbeat (mốc A2) — CHỈ ĐỂ THAM KHẢO.
//
// Không chấm mức: mỗi ghi nhận chỉ có mã (ví dụ "disk_full:/var") và nội dung. Máy "ok" (không có ghi nhận) hoặc
// "note" (có ghi nhận, để tham khảo). Tách hẳn khỏi cảnh báo: không ghi sự kiện, không mở giai đoạn cảnh báo, không
// tô viền siêu thị — chỉ hiện ở menu Sức khỏe máy, thẻ Tình trạng máy và cột Sức khỏe ở trang Máy; hết thì tự mất.
// (Bản 6963dd3 từng ghi sự kiện "may.<mã>"; migration 0017 đóng các cảnh báo chỉ gồm sự kiện đó.)

// HealthPrefix: tiền tố mã rule của sự kiện sức khỏe máy trong giai đoạn cũ (bản 6963dd3; nay không ghi nữa,
// giữ để hiện tiêu đề trong lịch sử).
const HealthPrefix = "may."

// Ngưỡng ghi nhận (đổi được khi test).
var (
	HealthDiskPct      = 90.0 // % đã dùng
	HealthInodePct     = 95.0
	HealthMemLowPct    = 5.0 // RAM còn dưới bấy nhiêu %…
	HealthMemLowStreak = 3   // …liên tiếp bấy nhiêu heartbeat
	HealthTempHigh     = 90  // °C
	HealthWearPct      = 90  // SSD đã dùng ≥ bấy nhiêu % tuổi thọ (SMART)
)

// Mức của máy: chỉ có hoặc không có ghi nhận.
const (
	HealthOK   = "ok"
	HealthNote = "note"
)

// HealthIssue: một ghi nhận về sức khỏe máy (tham khảo).
type HealthIssue struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// Health: sức khỏe máy theo heartbeat gần nhất.
type Health struct {
	Level  string        `json:"level"` // ok | note
	Issues []HealthIssue `json:"issues"`
	Smart  string        `json:"smart,omitempty"` // tình trạng đọc SMART của máy (SmartOK…), để thống kê và lọc
}

// Tình trạng đọc SMART của một máy (Health.Smart) — trả lời "máy này đã có SMART chưa, vì sao chưa".
const (
	SmartOK         = "ok"         // đọc được ít nhất một ổ
	SmartOld        = "old"        // agent bản cũ, chưa hỗ trợ SMART
	SmartMissing    = "missing"    // agent hỗ trợ nhưng chưa có kết quả: chưa cài tác vụ (install.sh --smart-only) / chưa chạy lần nào
	SmartError      = "error"      // tác vụ đã chạy nhưng file kết quả lỗi
	SmartStale      = "stale"      // kết quả cũ hơn SmartStaleAfter (tác vụ không chạy nữa?)
	SmartUnreadable = "unreadable" // có ổ nhưng không ổ nào đọc được (sau card RAID phần cứng, ổ USB…)
	SmartNoDisk     = "nodisk"     // không thấy ổ nào
)

// SmartStaleAfter: kết quả SMART cũ hơn bấy nhiêu thì coi như tác vụ không chạy (tác vụ chạy mỗi ngày).
var SmartStaleAfter = 3 * 24 * time.Hour

// smartState: tình trạng đọc SMART từ trạng thái máy (nil = agent không gửi trạng thái máy).
func smartState(st *protocol.MachineStatus, now time.Time) string {
	if st == nil {
		return ""
	}
	sm := st.Smart
	switch {
	case sm == nil:
		return SmartOld
	case sm.At.IsZero():
		return SmartMissing
	case sm.Error != "":
		return SmartError
	case now.Sub(sm.At) > SmartStaleAfter:
		return SmartStale
	case len(sm.Disks) == 0:
		return SmartNoDisk
	}
	for _, d := range sm.Disks {
		if d.OK {
			return SmartOK
		}
	}
	return SmartUnreadable
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

// evalHealth liệt kê ghi nhận từ trạng thái máy. memLow: số heartbeat liên tiếp RAM thấp (kể cả lần này).
func evalHealth(st *protocol.MachineStatus, memLow int) Health {
	h := Health{Level: HealthOK, Issues: []HealthIssue{}}
	if st == nil {
		return h
	}
	add := func(code, format string, a ...any) {
		h.Issues = append(h.Issues, HealthIssue{Code: code, Text: fmt.Sprintf(format, a...)})
	}
	for _, d := range st.Disks {
		if d.ReadOnly && rwFS[d.FS] {
			add("fs_ro:"+d.Mount, "Phân vùng %s (%s) đang chỉ đọc: không ghi được dữ liệu (thường do ổ gặp lỗi)", d.Mount, d.FS)
		}
		if p := usedPct(d.TotalBytes, d.FreeBytes); p >= HealthDiskPct {
			add("disk_full:"+d.Mount, "Phân vùng %s đầy %.0f%%", d.Mount, p)
		}
		if d.InodesTotal > 0 {
			if p := usedPct(d.InodesTotal, d.InodesFree); p >= HealthInodePct {
				add("inode_full:"+d.Mount, "Phân vùng %s dùng %.0f%% inode: còn dung lượng nhưng có thể không tạo được file mới", d.Mount, p)
			}
		}
	}
	for _, b := range st.Block {
		if b.IOErrKnown && b.IOErrors > 0 {
			add("io_err:"+b.Name, "Ổ %s%s có %d lỗi I/O từ lần khởi động", b.Name, modelSuffix(b.Model), b.IOErrors)
		}
	}
	for _, r := range st.RAID {
		if r.Degraded {
			add("raid:"+r.Name, "RAID %s (%s) thiếu ổ: %d/%d ổ hoạt động", r.Name, r.Level, r.Active, r.Disks)
		}
	}
	for _, s := range st.Services {
		if s.Active == "failed" || s.Active == "inactive" {
			add("svc:"+s.Name, "Dịch vụ %s không chạy (%s)", s.Name, s.Active)
		}
	}
	if st.TimeSync.Checked && !st.TimeSync.Synced {
		add("time", "Đồng hồ máy chưa đồng bộ NTP")
	}
	if memLow >= HealthMemLowStreak && st.MemTotalKB > 0 {
		add("mem", "RAM còn %.0f%% (%d lần gửi liên tiếp)", float64(st.MemAvailableKB)*100/float64(st.MemTotalKB), memLow)
	}
	if st.TempC >= HealthTempHigh {
		add("temp", "Nhiệt độ máy %d°C", st.TempC)
	}
	if sm := st.Smart; sm != nil {
		for _, d := range sm.Disks {
			if !d.OK {
				continue
			}
			ata := ""
			if d.Kind == "ata" {
				ata = " (ước tính theo thuộc tính hãng)"
			}
			if d.Passed != nil && !*d.Passed {
				add("smart_fail:"+d.Name, "Ổ %s tự báo sắp hỏng (SMART): %s", d.Name, strings.Join(d.Failing, "; "))
			}
			if d.PercentUsed != nil && *d.PercentUsed >= HealthWearPct {
				add("smart_wear:"+d.Name, "Ổ %s đã dùng %d%% tuổi thọ%s", d.Name, *d.PercentUsed, ata)
			}
			if d.MediaErrors != nil && *d.MediaErrors > 0 {
				add("smart_media:"+d.Name, "Ổ %s có %d lỗi media (NVMe)", d.Name, *d.MediaErrors)
			}
			var parts []string
			for _, x := range []struct {
				v    *int64
				name string
			}{{d.Reallocated, "sector đã thay"}, {d.Pending, "sector chờ thay"}, {d.Uncorrectable, "sector không đọc được"}, {d.ReportedUncor, "lỗi không sửa được"}} {
				if x.v != nil && *x.v > 0 {
					parts = append(parts, fmt.Sprintf("%d %s", *x.v, x.name))
				}
			}
			if len(parts) > 0 {
				add("smart_sector:"+d.Name, "Ổ %s: %s (SMART)", d.Name, strings.Join(parts, ", "))
			}
			if d.CRCErrors != nil && *d.CRCErrors > 0 {
				add("smart_crc:"+d.Name, "Ổ %s có %d lỗi đường truyền (CRC, cộng dồn cả đời ổ): kiểm tra cáp / cổng SATA nếu số này tăng", d.Name, *d.CRCErrors)
			}
		}
	}
	if len(h.Issues) > 0 {
		h.Level = HealthNote
	}
	h.Smart = smartState(st, time.Now())
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
	if c.Smart != nil {
		sm := *c.Smart
		sm.Error = str(sm.Error, 300)
		sm.Disks = append([]protocol.SmartDisk(nil), clipList(sm.Disks, protocol.MaxSmart)...)
		for i := range sm.Disks {
			d := &sm.Disks[i]
			d.Name, d.Kind, d.Note = str(d.Name, 32), str(d.Kind, 8), str(d.Note, 300)
			d.TempC = min(max(d.TempC, -40), 200)
			d.Failing = clipList(d.Failing, 30)
			for j := range d.Failing {
				d.Failing[j] = str(d.Failing[j], 120)
			}
			for _, p := range []*int64{d.PowerOnHours, d.PowerCycles, d.MediaErrors, d.UnsafeShutdowns, d.Reallocated, d.Pending,
				d.Uncorrectable, d.ReportedUncor, d.CRCErrors} {
				if p != nil {
					*p = min(max(*p, 0), 1<<50)
				}
			}
			for _, p := range []*int{d.PercentUsed, d.AvailSpare} {
				if p != nil {
					*p = min(max(*p, 0), 255)
				}
			}
		}
		c.Smart = &sm
	}
	return &c
}

func clipList[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

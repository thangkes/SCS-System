package app

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/common/protocol"
)

func codes(h Health) map[string]bool {
	m := map[string]bool{}
	for _, is := range h.Issues {
		m[is.Code] = true
	}
	return m
}

// Ghi nhận sức khỏe (tham khảo, không chấm mức): đúng ngưỡng; máy không có ghi nhận = ok, có = note.
func TestEvalHealth(t *testing.T) {
	const G = 1 << 30
	good := &protocol.MachineStatus{MemTotalKB: 8 << 20, MemAvailableKB: 4 << 20, TempC: 50,
		Disks:    []protocol.DiskStatus{{Mount: "/", FS: "ext4", TotalBytes: 100 * G, FreeBytes: 40 * G, InodesTotal: 1000, InodesFree: 900}},
		Block:    []protocol.BlockDevice{{Name: "sda", IOErrKnown: true}, {Name: "nvme0n1"}},
		RAID:     []protocol.RAIDStatus{{Name: "md0", Disks: 2, Active: 2}},
		TimeSync: protocol.TimeSyncStatus{Synced: true, Checked: true},
		Services: []protocol.ServiceStatus{{Name: "pos.service", Active: "active"}}}
	if h := evalHealth(good, 0); h.Level != HealthOK || len(h.Issues) != 0 {
		t.Fatalf("không có ghi nhận: %+v", h)
	}
	if h := evalHealth(nil, 0); h.Level != HealthOK {
		t.Fatalf("không có trạng thái: %+v", h)
	}

	bad := *good
	bad.Disks = []protocol.DiskStatus{
		{Mount: "/", FS: "ext4", TotalBytes: 100 * G, FreeBytes: 4 * G},                                     // 96%
		{Mount: "/var", FS: "ext4", TotalBytes: 100 * G, FreeBytes: 8 * G, InodesTotal: 100, InodesFree: 2}, // 92%, inode 98%
		{Mount: "/data", FS: "xfs", TotalBytes: 100 * G, FreeBytes: 50 * G, ReadOnly: true},                 // chỉ đọc
		{Mount: "/boot/efi", FS: "vfat", TotalBytes: G, FreeBytes: G / 2, ReadOnly: true},                   // vfat chỉ đọc: bỏ qua
		{Mount: "/opt", FS: "ext4", TotalBytes: 100 * G, FreeBytes: 11 * G},                                 // 89%: chưa tới ngưỡng
	}
	bad.Block = []protocol.BlockDevice{{Name: "sda", Model: "WDC", IOErrKnown: true, IOErrors: 3}}
	bad.RAID = []protocol.RAIDStatus{{Name: "md0", Level: "raid1", Disks: 2, Active: 1, Degraded: true}}
	bad.Services = []protocol.ServiceStatus{{Name: "pos.service", Active: "failed"}, {Name: "x.service", Active: "activating"}}
	bad.TimeSync = protocol.TimeSyncStatus{Checked: true}
	bad.MemAvailableKB = bad.MemTotalKB / 50 // 2%
	bad.TempC = 92
	h := evalHealth(&bad, 3)
	want := []string{"disk_full:/", "disk_full:/var", "inode_full:/var", "fs_ro:/data", "io_err:sda", "raid:md0",
		"svc:pos.service", "time", "mem", "temp"}
	got := codes(h)
	if h.Level != HealthNote || len(got) != len(want) {
		t.Fatalf("ghi nhận: %v (muốn %v)", got, want)
	}
	for _, k := range want {
		if !got[k] {
			t.Errorf("thiếu %s", k)
		}
	}
	if !strings.Contains(codesText(h, "io_err:sda"), "3 lỗi I/O") {
		t.Fatalf("nội dung: %+v", h.Issues)
	}
	if b, _ := json.Marshal(h); strings.Contains(string(b), "severity") {
		t.Fatalf("không còn mức đánh giá: %s", b)
	}
	// RAM thấp chưa đủ 3 lần liên tiếp: chưa ghi nhận.
	only := *good
	only.TimeSync = protocol.TimeSyncStatus{Checked: true}
	only.MemAvailableKB = only.MemTotalKB / 50
	if h := evalHealth(&only, 2); h.Level != HealthNote || len(h.Issues) != 1 || h.Issues[0].Code != "time" {
		t.Fatalf("RAM chưa đủ 3 lần: %+v", h)
	}
}

func codesText(h Health, code string) string {
	for _, is := range h.Issues {
		if is.Code == code {
			return is.Text
		}
	}
	return ""
}

func TestLowMemStreak(t *testing.T) {
	const id = 987654
	for i := 1; i <= 3; i++ {
		if n := lowMemStreak(id, true); n != i {
			t.Fatalf("lần %d: %d", i, n)
		}
	}
	if lowMemStreak(id, false) != 0 || lowMemStreak(id, true) != 1 {
		t.Fatal("RAM hết thấp thì đếm lại")
	}
}

func TestHealthTitle(t *testing.T) {
	for id, want := range map[string]string{"may.disk_full:/var": "Phân vùng /var sắp đầy", "may.io_err:sda": "Ổ sda có lỗi I/O",
		"may.svc:pos.service": "Dịch vụ pos.service không chạy", "ssh-brute": ""} {
		if got := HealthTitle(id); got != want {
			t.Errorf("%s: %q, muốn %q", id, got, want)
		}
	}
}

// Không tin agent: quá số phần tử thì cắt, chuỗi có ký tự điều khiển / quá dài thì làm sạch.
func TestCleanStatus(t *testing.T) {
	st := &protocol.MachineStatus{Hostname: "a\x00b", Load: []float64{1, 2, 3, 4}, TempC: 999, CPUs: -1}
	for i := 0; i < 40; i++ {
		st.Disks = append(st.Disks, protocol.DiskStatus{Mount: "/m" + strings.Repeat("x", 300)})
		st.Block = append(st.Block, protocol.BlockDevice{Name: "sd", Model: "<b>\x1b[31m" + strings.Repeat("y", 100), IOErrors: -5})
		st.Errors = append(st.Errors, "e")
	}
	c := cleanStatus(st)
	if c.Hostname != "ab" || len(c.Load) != 3 || c.TempC != 200 || c.CPUs != 0 {
		t.Fatalf("trường đơn: %+v", c)
	}
	if len(c.Disks) != protocol.MaxDisks || len(c.Block) != protocol.MaxBlock || len(c.Errors) != protocol.MaxErrors {
		t.Fatalf("số phần tử: %d %d %d", len(c.Disks), len(c.Block), len(c.Errors))
	}
	if len(c.Disks[0].Mount) > 128 || len(c.Block[0].Model) > 64 || strings.ContainsRune(c.Block[0].Model, 0x1b) || c.Block[0].IOErrors != 0 {
		t.Fatalf("chuỗi: %q %q %d", c.Disks[0].Mount, c.Block[0].Model, c.Block[0].IOErrors)
	}
	if cleanStatus(nil) != nil {
		t.Fatal("nil")
	}
}

// Qua handler: trạng thái agent gửi được làm sạch và tính sức khỏe trước khi ghi.
func TestHeartbeatComputesHealth(t *testing.T) {
	d, reg := enrollDeps(t)
	a := newTestAgent(t, "12345-TN3")
	cert, _ := approvedAgent(t, d, reg, a)
	id, _ := strconv.ParseInt(cert.Subject.CommonName, 10, 64)
	w, _ := postHeartbeat(d, cert, heartbeatPacket(t, a, cert.Subject.CommonName, func(e *protocol.Envelope) {
		e.Body, _ = json.Marshal(protocol.HeartbeatBody{UptimeS: 60, Status: &protocol.MachineStatus{OS: "Ubuntu",
			RAID: []protocol.RAIDStatus{{Name: "md0", Level: "raid1", Disks: 2, Active: 1, Degraded: true}}}})
	}))
	if w.Code != 200 {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body)
	}
	reg.mu.Lock()
	h := reg.status[id].Health
	reg.mu.Unlock()
	if h == nil || h.Level != HealthNote || len(h.Issues) != 1 || h.Issues[0].Code != "raid:md0" {
		t.Fatalf("health: %+v", h)
	}
}

// SMART: ghi nhận tham khảo khi ổ tự báo hỏng, SSD mòn ≥ 90%, lỗi media, sector hỏng, lỗi CRC; ổ không đọc được
// SMART thì không ghi nhận gì.
func TestHealthSmart(t *testing.T) {
	no, yes := false, true
	n := func(v int64) *int64 { return &v }
	p := func(v int) *int { return &v }
	st := &protocol.MachineStatus{Smart: &protocol.SmartReport{Disks: []protocol.SmartDisk{
		{Name: "sda", Kind: "ata", OK: true, Passed: &no, Failing: []string{"sector đã thay (5) 9 ≤ ngưỡng 10"},
			Reallocated: n(4000), Pending: n(3), CRCErrors: n(41), PercentUsed: p(92), WearAttr: 231},
		{Name: "nvme0n1", Kind: "nvme", OK: true, Passed: &yes, PercentUsed: p(12), MediaErrors: n(2)},
		{Name: "sdb", Kind: "ata", OK: false, Note: "ổ ảo của card RAID phần cứng"},
		{Name: "sdc", Kind: "ata", OK: true, Passed: &yes, Reallocated: n(0), CRCErrors: n(0), PercentUsed: p(40)},
	}}}
	h := evalHealth(st, 0)
	got := codes(h)
	for _, c := range []string{"smart_fail:sda", "smart_wear:sda", "smart_sector:sda", "smart_crc:sda", "smart_media:nvme0n1"} {
		if !got[c] {
			t.Errorf("thiếu ghi nhận %s: %v", c, got)
		}
	}
	if len(got) != 5 || h.Level != HealthNote {
		t.Fatalf("chỉ 5 ghi nhận (ổ RAID / ổ khỏe không ghi gì): %v", got)
	}
	if !strings.Contains(codesText(h, "smart_wear:sda"), "ước tính") || !strings.Contains(codesText(h, "smart_sector:sda"), "4000 sector đã thay, 3 sector chờ thay") {
		t.Fatalf("nội dung: %+v", h.Issues)
	}
	// Làm sạch: cắt số ổ, chặn số âm, bỏ ký tự điều khiển.
	bad := &protocol.MachineStatus{Smart: &protocol.SmartReport{Error: "x\x00y"}}
	for i := 0; i < 40; i++ {
		bad.Smart.Disks = append(bad.Smart.Disks, protocol.SmartDisk{Name: "sd\x1b", Reallocated: n(-5), PercentUsed: p(999)})
	}
	c := cleanStatus(bad)
	if len(c.Smart.Disks) != protocol.MaxSmart || c.Smart.Error != "xy" || c.Smart.Disks[0].Name != "sd" ||
		*c.Smart.Disks[0].Reallocated != 0 || *c.Smart.Disks[0].PercentUsed != 255 {
		t.Fatalf("làm sạch SMART: %+v", c.Smart)
	}
}

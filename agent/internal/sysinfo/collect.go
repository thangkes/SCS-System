package sysinfo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/thangkes/SCS-System/common/fingerprint"
	"github.com/thangkes/SCS-System/common/protocol"
)

// Root là gốc hệ thống file khi đọc /proc, /sys, /etc (test đổi sang thư mục mẫu).
var Root = "/"

// run gọi lệnh hệ thống (timedatectl, systemctl) có giới hạn thời gian. Test thay được.
var run = func(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func path(p string) string { return filepath.Join(Root, filepath.FromSlash(p)) }

func readTrim(p string) (string, error) {
	b, err := os.ReadFile(path(p))
	return strings.TrimSpace(string(b)), err
}

// Components đọc các thành phần fingerprint. Serial phần cứng chỉ root (hoặc quyền
// CAP_DAC_READ_SEARCH) đọc được; thiếu thành phần nào thì để trống và báo trong errs.
func Components() (c fingerprint.Components, errs []string) {
	var err error
	if c.MachineID, err = readTrim("etc/machine-id"); err != nil || c.MachineID == "" {
		if c.MachineID, err = readTrim("var/lib/dbus/machine-id"); err != nil {
			errs = append(errs, "không đọc được machine-id")
		}
	}
	if c.BoardSerial, err = readTrim("sys/class/dmi/id/board_serial"); err != nil {
		errs = append(errs, "không đọc được serial mainboard ("+reason(err)+")")
	}
	if c.SystemSerial, err = readTrim("sys/class/dmi/id/product_serial"); err != nil {
		errs = append(errs, "không đọc được serial hệ thống ("+reason(err)+")")
	}
	if c.PrimaryMAC, err = PrimaryMAC(); err != nil {
		errs = append(errs, "không đọc được MAC card mạng chính ("+reason(err)+")")
	}
	return c, errs
}

var ifaceName = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)

// PrimaryMAC: MAC của card mạng đi ra tuyến mặc định.
func PrimaryMAC() (string, error) {
	f, err := os.Open(path("proc/net/route"))
	if err != nil {
		return "", err
	}
	iface := parseDefaultRouteIface(f)
	f.Close()
	if iface == "" {
		return "", errors.New("không có tuyến mặc định")
	}
	if !ifaceName.MatchString(iface) {
		return "", errors.New("tên card mạng lạ")
	}
	return readTrim("sys/class/net/" + iface + "/address")
}

func reason(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "không đủ quyền"
	case errors.Is(err, os.ErrNotExist):
		return "không có"
	}
	return "lỗi đọc"
}

// Hostname của máy (như kernel đang dùng).
func Hostname() string {
	h, _ := os.Hostname()
	return h
}

// OSVersion: tên bản Ubuntu, ví dụ "Ubuntu 22.04.4 LTS".
func OSVersion() string {
	f, err := os.Open(path("etc/os-release"))
	if err != nil {
		return ""
	}
	defer f.Close()
	return parseOSRelease(f)
}

// Uptime: số giây máy đã chạy.
func Uptime() (int64, error) {
	s, err := readTrim("proc/uptime")
	if err != nil {
		return 0, err
	}
	return parseUptime(s)
}

// Status đọc trạng thái máy cho heartbeat. Phần nào lỗi thì để trống và ghi lý do,
// không làm hỏng cả heartbeat.
func Status(services []string) *protocol.MachineStatus {
	st := &protocol.MachineStatus{Hostname: Hostname(), OS: OSVersion(), CPUs: runtime.NumCPU()}
	fail := func(what string, err error) { st.Errors = append(st.Errors, what+": "+reason(err)) }

	if k, err := readTrim("proc/sys/kernel/osrelease"); err == nil {
		st.Kernel = k
	} else {
		fail("kernel", err)
	}
	if s, err := readTrim("proc/loadavg"); err == nil {
		if st.Load, err = parseLoadavg(s); err != nil {
			fail("loadavg", err)
		}
	} else {
		fail("loadavg", err)
	}
	if f, err := os.Open(path("proc/meminfo")); err == nil {
		m := parseMeminfo(f)
		f.Close()
		st.MemTotalKB, st.MemAvailableKB = m["MemTotal"], m["MemAvailable"]
		st.SwapTotalKB, st.SwapFreeKB = m["SwapTotal"], m["SwapFree"]
	} else {
		fail("meminfo", err)
	}
	if f, err := os.Open(path("proc/self/mounts")); err == nil {
		for _, m := range parseMounts(f) {
			if len(st.Disks) == protocol.MaxDisks {
				break
			}
			u, err := diskUsage(m.Path)
			if err != nil {
				fail("dung lượng "+m.Path, err)
				continue
			}
			st.Disks = append(st.Disks, protocol.DiskStatus{Mount: m.Path, FS: m.FS, Device: m.Device, TotalBytes: u.total,
				FreeBytes: u.free, InodesTotal: u.inodes, InodesFree: u.inodesFree, ReadOnly: m.ReadOnly})
		}
		f.Close()
	} else {
		fail("mounts", err)
	}
	if up, err := Uptime(); err == nil {
		st.BootUptimeS = up
	}
	st.Block = blockDevices()
	st.TempC = maxTemp()
	if f, err := os.Open(path("proc/mdstat")); err == nil { // không có RAID phần mềm thì không có file
		st.RAID = parseMdstat(f)
		f.Close()
	}
	if out, err := run("timedatectl", "show", "-p", "NTPSynchronized", "-p", "NTP"); err == nil {
		p := parseProps(out)
		st.TimeSync = protocol.TimeSyncStatus{Synced: p["NTPSynchronized"] == "yes", NTPOn: p["NTP"] == "yes", Checked: true}
	} else {
		st.Errors = append(st.Errors, "timedatectl: không chạy được")
	}
	for i, name := range services {
		if i == protocol.MaxServices {
			break
		}
		s := protocol.ServiceStatus{Name: name, Active: "unknown"}
		// Tên dịch vụ đã được config kiểm (không bắt đầu bằng "-"), gọi thẳng không qua shell.
		if out, err := run("systemctl", "show", "-p", "ActiveState", "-p", "SubState", name); err == nil {
			p := parseProps(out)
			if p["ActiveState"] != "" {
				s.Active, s.Sub = p["ActiveState"], p["SubState"]
			}
		}
		st.Services = append(st.Services, s)
	}
	if len(st.Errors) > protocol.MaxErrors {
		st.Errors = st.Errors[:protocol.MaxErrors]
	}
	return st
}

// blockDevices: ổ vật lý trong /sys/block (đọc được bằng user thường, không cần quyền với /dev). Bỏ ổ ảo
// (loop, ram, zram), lớp ảo (dm, md — RAID báo riêng), ổ quang / ổ rời (USB).
func blockDevices() []protocol.BlockDevice {
	ents, err := os.ReadDir(path("sys/block"))
	if err != nil {
		return nil
	}
	var out []protocol.BlockDevice
	for _, e := range ents {
		name := e.Name()
		if skipBlock(name) || len(out) == protocol.MaxBlock {
			continue
		}
		base := "sys/block/" + name + "/"
		if rm, _ := readTrim(base + "removable"); rm == "1" {
			continue
		}
		b := protocol.BlockDevice{Name: name}
		if s, err := readTrim(base + "size"); err == nil {
			if n, err := strconv.ParseUint(s, 10, 64); err == nil {
				b.SizeBytes = n * 512 // /sys/block/*/size luôn tính theo sector 512 byte
			}
		}
		if b.SizeBytes == 0 {
			continue // khe thẻ nhớ trống, ổ chưa sẵn sàng
		}
		if r, err := readTrim(base + "queue/rotational"); err == nil {
			b.SSD = r == "0"
		}
		if m, err := readTrim(base + "device/model"); err == nil {
			b.Model = cleanModel(m)
		}
		if s, err := readTrim(base + "device/ioerr_cnt"); err == nil { // dạng "0x1a"
			if n, err := strconv.ParseInt(s, 0, 64); err == nil && n >= 0 {
				b.IOErrors, b.IOErrKnown = n, true
			}
		}
		out = append(out, b)
	}
	return out
}

func skipBlock(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "dm-", "md", "sr", "fd", "nbd", "mmcblk0boot", "mmcblk1boot"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// cleanModel: model ổ chỉ giữ ký tự in được, tối đa 64 ký tự.
func cleanModel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// maxTemp: nhiệt độ cao nhất (°C) các vùng nhiệt (/sys/class/thermal); 0 = không có cảm biến.
func maxTemp() int {
	ents, err := os.ReadDir(path("sys/class/thermal"))
	if err != nil {
		return 0
	}
	best := 0
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "thermal_zone") {
			continue
		}
		s, err := readTrim("sys/class/thermal/" + e.Name() + "/temp")
		if err != nil {
			continue
		}
		if mc, err := strconv.Atoi(s); err == nil && mc > 0 && mc < 130000 && mc/1000 > best {
			best = mc / 1000
		}
	}
	return best
}

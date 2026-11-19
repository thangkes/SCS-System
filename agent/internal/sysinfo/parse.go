// Package sysinfo đọc thông tin máy: thành phần fingerprint, hostname, trạng thái hệ thống.
//
// Phần phân tích nội dung (/proc, /sys, kết quả systemctl...) là hàm thuần, test được trên
// mọi hệ điều hành; phần đọc file và gọi lệnh nằm ở collect.go.
package sysinfo

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/thangkes/SCS-System/common/protocol"
)

// parseLoadavg đọc /proc/loadavg: "0.52 0.58 0.59 1/1234 5678".
func parseLoadavg(s string) ([]float64, error) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return nil, fmt.Errorf("loadavg sai định dạng")
	}
	out := make([]float64, 3)
	for i := range out {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return nil, fmt.Errorf("loadavg sai định dạng")
		}
		out[i] = v
	}
	return out, nil
}

// parseUptime đọc /proc/uptime: "18230.52 70000.11" → giây.
func parseUptime(s string) (int64, error) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, fmt.Errorf("uptime sai định dạng")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("uptime sai định dạng")
	}
	return int64(v), nil
}

// parseMeminfo đọc /proc/meminfo, trả về các dòng "Tên: số kB".
func parseMeminfo(r io.Reader) map[string]uint64 {
	out := map[string]uint64{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		if n, err := strconv.ParseUint(f[0], 10, 64); err == nil {
			out[strings.TrimSpace(k)] = n
		}
	}
	return out
}

// parseOSRelease lấy PRETTY_NAME trong /etc/os-release.
func parseOSRelease(r io.Reader) string {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// parseDefaultRouteIface tìm card mạng của tuyến mặc định trong /proc/net/route
// (Destination và Mask đều 00000000; nhiều tuyến thì lấy Metric nhỏ nhất).
func parseDefaultRouteIface(r io.Reader) string {
	sc := bufio.NewScanner(r)
	best, bestMetric := "", -1
	for first := true; sc.Scan(); first = false {
		f := strings.Fields(sc.Text())
		if first || len(f) < 8 {
			continue // dòng tiêu đề
		}
		if f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		metric, err := strconv.Atoi(f[6])
		if err != nil {
			continue
		}
		if bestMetric < 0 || metric < bestMetric {
			best, bestMetric = f[0], metric
		}
	}
	return best
}

// Loại hệ thống file của ổ thật; bỏ tmpfs, overlay, squashfs (snap)...
var diskFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"f2fs": true, "vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "fuseblk": true,
}

type mount struct {
	Device, Path, FS string
	ReadOnly         bool
}

// usage: dung lượng và inode của một phân vùng (statfs).
type usage struct{ total, free, inodes, inodesFree uint64 }

// parseMounts đọc /proc/self/mounts, giữ phân vùng ổ thật, mỗi thiết bị một lần.
func parseMounts(r io.Reader) []mount {
	var out []mount
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || !diskFS[f[2]] || seen[f[0]] {
			continue
		}
		path := unescapeMount(f[1])
		if strings.HasPrefix(path, "/snap/") {
			continue
		}
		seen[f[0]] = true
		ro := false
		if len(f) > 3 {
			for _, o := range strings.Split(f[3], ",") {
				ro = ro || o == "ro"
			}
		}
		out = append(out, mount{Device: f[0], Path: path, FS: f[2], ReadOnly: ro})
		if len(out) == protocol.MaxDisks {
			break
		}
	}
	return out
}

// unescapeMount: /proc/mounts viết khoảng trắng, tab... dạng \040, \011.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var (
	mdHead   = regexp.MustCompile(`^(md[0-9]+)\s*:\s*(\S+)(?:\s+\(\S+\))?\s*(\S*)`)
	mdCount  = regexp.MustCompile(`\[(\d+)/(\d+)\]\s*\[([U_]+)\]`)
	mdResync = regexp.MustCompile(`(recovery|resync|reshape|check)\s*=\s*([0-9.]+%)`)
)

// parseMdstat đọc /proc/mdstat (RAID phần mềm).
func parseMdstat(r io.Reader) []protocol.RAIDStatus {
	var out []protocol.RAIDStatus
	var cur *protocol.RAIDStatus
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if m := mdHead.FindStringSubmatch(line); m != nil {
			if len(out) == protocol.MaxRAID {
				break
			}
			level := m[3]
			if !strings.HasPrefix(level, "raid") && level != "linear" {
				level = "" // mảng inactive không ghi level
			}
			out = append(out, protocol.RAIDStatus{Name: m[1], Level: level, Degraded: m[2] != "active"})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := mdCount.FindStringSubmatch(line); m != nil {
			cur.Disks, _ = strconv.Atoi(m[1])
			cur.Active, _ = strconv.Atoi(m[2])
			cur.Degraded = cur.Degraded || cur.Active < cur.Disks || strings.Contains(m[3], "_")
		}
		if m := mdResync.FindStringSubmatch(line); m != nil {
			cur.Sync = m[1] + " " + m[2]
		}
		if strings.TrimSpace(line) == "" {
			cur = nil
		}
	}
	return out
}

// parseProps đọc kết quả "systemctl show" / "timedatectl show": các dòng Key=Value.
func parseProps(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			out[k] = v
		}
	}
	return out
}

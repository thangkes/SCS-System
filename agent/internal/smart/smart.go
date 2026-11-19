// Package smart đọc SMART của ổ vật lý (NVMe và SATA) mà không cần smartmontools.
//
// Đọc SMART phải gửi lệnh thẳng tới ổ (ioctl SG_IO cho SATA cần CAP_SYS_RAWIO, NVMe admin cần CAP_SYS_ADMIN),
// nên KHÔNG chạy trong tiến trình agent thường. Lệnh `scs-agent smart` chạy bằng một tác vụ root riêng
// (scs-agent-smart.timer, mỗi ngày) với đúng hai quyền đó, chỉ mở thiết bị ổ ở chế độ chỉ đọc, không có mạng,
// chỉ ghi được một thư mục kết quả (root:scs-agent 750). Agent thường chỉ ĐỌC file kết quả và gửi lên server.
//
// Đọc được: NVMe (log SMART chuẩn: cảnh báo nghiêm trọng, % tuổi thọ đã dùng, lỗi media, nhiệt độ, giờ chạy),
// SATA (bảng thuộc tính: sector đã thay / chờ thay / không đọc được, lỗi đường truyền CRC, giờ chạy, nhiệt độ,
// độ mòn SSD theo thuộc tính hãng). Không đọc được: ổ ảo của card RAID phần cứng, phần lớn ổ USB, ổ SAS.
package smart

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

// DefaultFile: file kết quả (thư mục root:scs-agent 750 do bộ cài tạo; agent không ghi được).
const DefaultFile = "/var/lib/scs-agent-smart/smart.json"

const maxFile = 64 << 10

// Root: gốc hệ thống file khi đọc /sys (test đổi được).
var Root = "/"

func sysPath(p string) string { return filepath.Join(Root, filepath.FromSlash(p)) }

func readTrim(p string) string {
	b, err := os.ReadFile(sysPath(p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Ổ SATA: đọc bằng SG_IO. Ổ NVMe: /dev/nvmeX (bộ điều khiển) của nvmeXnY.
var nvmeRe = regexp.MustCompile(`^(nvme[0-9]+)n[0-9]+$`)

// Bộ chuyển của card RAID phần cứng: Linux chỉ thấy ổ ảo, không chuyển lệnh SMART tới ổ thật.
var raidVendors = []string{"LSI", "AVAGO", "BROADCOM", "DELL", "HP", "HPE", "ADAPTEC", "MEGARAID", "PERC", "INTEL RAID", "IBM-ESXS", "LENOVO"}
var raidModels = []string{"LOGICAL", "PERC", "MR9", "RAID", "MEGARAID", "SMART ARRAY", "VIRTUAL"}

// devIO: phần gọi ioctl (dev_linux.go); test thay được.
var (
	readATA  = readATAOS  // (device, có lỗi) → SMART READ DATA 512 byte + READ THRESHOLDS 512 byte
	readNVMe = readNVMeOS // (device) → log SMART / health 512 byte
)

// Read đọc SMART mọi ổ vật lý trong /sys/block (bỏ ổ ảo, ổ quang, ổ rời).
func Read(now time.Time) protocol.SmartReport {
	rep := protocol.SmartReport{At: now.UTC()}
	ents, err := os.ReadDir(sysPath("sys/block"))
	if err != nil {
		rep.Error = "không đọc được /sys/block: " + err.Error()
		return rep
	}
	for _, e := range ents {
		name := e.Name()
		if skip(name) || len(rep.Disks) == protocol.MaxSmart {
			continue
		}
		if readTrim("sys/block/"+name+"/removable") == "1" {
			continue
		}
		rep.Disks = append(rep.Disks, readDisk(name))
	}
	return rep
}

func skip(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "dm-", "md", "sr", "fd", "nbd", "mmcblk"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func readDisk(name string) protocol.SmartDisk {
	if m := nvmeRe.FindStringSubmatch(name); m != nil {
		d := protocol.SmartDisk{Name: name, Kind: "nvme"}
		buf, err := readNVMe("/dev/" + m[1])
		if err != nil {
			d.Note = "không đọc được log SMART NVMe: " + err.Error()
			return d
		}
		parseNVMe(&d, buf)
		return d
	}
	d := protocol.SmartDisk{Name: name, Kind: "ata"}
	if !strings.HasPrefix(name, "sd") {
		d.Note = "loại ổ chưa hỗ trợ đọc SMART"
		return d
	}
	if link, err := os.Readlink(sysPath("sys/block/" + name)); err == nil && strings.Contains(link, "/usb") {
		d.Note = "ổ gắn qua USB: bộ chuyển thường không chuyển lệnh SMART"
		return d
	}
	vendor := strings.ToUpper(readTrim("sys/block/" + name + "/device/vendor"))
	model := strings.ToUpper(readTrim("sys/block/" + name + "/device/model"))
	if hasAny(vendor, raidVendors) && hasAny(model, raidModels) || hasAny(model, []string{"LOGICAL VOLUME", "PERC", "MR9", "SMART ARRAY"}) {
		d.Note = "ổ ảo của card RAID phần cứng: không đọc được SMART của ổ thật (xem bằng công cụ của hãng card)"
		return d
	}
	data, thr, err := readATA("/dev/" + name)
	if err != nil {
		d.Note = "không đọc được SMART qua ATA pass-through (ổ SAS / SCSI, sau card RAID, hoặc ổ không hỗ trợ): " + err.Error()
		return d
	}
	parseATA(&d, data, thr)
	return d
}

func hasAny(s string, list []string) bool {
	for _, x := range list {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func i64(v int64) *int64 { return &v }
func iptr(v int) *int    { return &v }

// parseNVMe đọc log SMART / Health Information (log 02h, 512 byte, NVMe 1.x).
func parseNVMe(d *protocol.SmartDisk, b []byte) {
	if len(b) < 192 {
		d.Note = "log SMART NVMe quá ngắn"
		return
	}
	d.OK = true
	d.CriticalWarning = int(b[0])
	if k := int(binary.LittleEndian.Uint16(b[1:3])); k > 0 {
		d.TempC = k - 273
	}
	d.AvailSpare = iptr(int(b[3]))
	d.PercentUsed = iptr(int(b[5])) // có thể > 100 khi đã vượt tuổi thọ hãng công bố
	d.PowerCycles = i64(u128lo(b[112:128]))
	d.PowerOnHours = i64(u128lo(b[128:144]))
	d.UnsafeShutdowns = i64(u128lo(b[144:160]))
	d.MediaErrors = i64(u128lo(b[160:176]))
	passed := d.CriticalWarning == 0
	d.Passed = &passed
	if d.CriticalWarning != 0 {
		d.Failing = append(d.Failing, nvmeWarnings(d.CriticalWarning)...)
	}
}

// u128lo: 64 bit thấp của số 128 bit little-endian (đủ cho mọi giá trị thực tế), chặn âm.
func u128lo(b []byte) int64 {
	v := binary.LittleEndian.Uint64(b[:8])
	if v > 1<<62 || binary.LittleEndian.Uint64(b[8:16]) != 0 {
		return 1 << 62
	}
	return int64(v)
}

func nvmeWarnings(w int) []string {
	names := []string{"dự phòng dưới ngưỡng", "nhiệt độ vượt ngưỡng", "độ tin cậy suy giảm", "chuyển sang chỉ đọc", "bộ nhớ đệm dự phòng hỏng", "vùng PMR không tin cậy"}
	var out []string
	for i, n := range names {
		if w&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

// Thuộc tính độ mòn SSD SATA (giá trị chuẩn hóa = % tuổi thọ còn lại, tùy hãng).
var wearAttrs = []int{231, 233, 177, 169, 202}

var attrName = map[int]string{1: "lỗi đọc", 3: "thời gian quay", 5: "sector đã thay", 7: "lỗi định vị", 10: "quay lại", 196: "sự kiện thay sector",
	197: "sector chờ thay", 198: "sector không sửa được", 231: "tuổi thọ SSD", 233: "độ mòn", 177: "độ mòn", 169: "tuổi thọ còn lại", 202: "tuổi thọ còn lại"}

// parseATA đọc bảng thuộc tính SMART (SMART READ DATA) và ngưỡng (SMART READ THRESHOLDS).
func parseATA(d *protocol.SmartDisk, data, thr []byte) {
	if len(data) < 362 {
		d.Note = "dữ liệu SMART ATA quá ngắn"
		return
	}
	thresh := map[int]int{}
	if len(thr) >= 362 {
		for i := 0; i < 30; i++ {
			o := 2 + 12*i
			if id := int(thr[o]); id != 0 {
				thresh[id] = int(thr[o+1])
			}
		}
	}
	d.OK = true
	passed := true
	for i := 0; i < 30; i++ {
		o := 2 + 12*i
		id := int(data[o])
		if id == 0 {
			continue
		}
		flags := binary.LittleEndian.Uint16(data[o+1 : o+3])
		value := int(data[o+3])
		raw := int64(binary.LittleEndian.Uint32(data[o+5:o+9])) | int64(binary.LittleEndian.Uint16(data[o+9:o+11]))<<32
		if t, ok := thresh[id]; ok && t > 0 && value <= t {
			name := attrName[id]
			if name == "" {
				name = "thuộc tính"
			}
			d.Failing = append(d.Failing, fmt.Sprintf("%s (%d) %d ≤ ngưỡng %d", name, id, value, t))
			if flags&1 != 0 { // pre-fail: ổ báo sắp hỏng
				passed = false
			}
		}
		switch id {
		case 5:
			d.Reallocated = i64(raw & 0xffffffff)
		case 9:
			d.PowerOnHours = i64(raw & 0xffffffff)
		case 12:
			d.PowerCycles = i64(raw & 0xffffffff)
		case 187:
			d.ReportedUncor = i64(raw & 0xffffffff)
		case 194:
			if t := int(raw & 0xff); t > 0 && t < 130 {
				d.TempC = t
			}
		case 190:
			if d.TempC == 0 {
				if t := int(raw & 0xff); t > 0 && t < 130 {
					d.TempC = t
				}
			}
		case 197:
			d.Pending = i64(raw & 0xffffffff)
		case 198:
			d.Uncorrectable = i64(raw & 0xffffffff)
		case 199:
			d.CRCErrors = i64(raw & 0xffffffff)
		}
		if d.WearAttr == 0 && value > 0 && value <= 100 {
			for _, w := range wearAttrs {
				if id == w {
					d.WearAttr = id
					d.PercentUsed = iptr(100 - value)
				}
			}
		}
	}
	d.Passed = &passed
}

// Save ghi kết quả (file tạm trong cùng thư mục rồi đổi tên). Thư mục phải thuộc root và người khác không ghi
// được: agent không thay được file bằng symlink để lừa tác vụ root ghi đè chỗ khác.
func Save(path string, rep protocol.SmartReport) error {
	dir := filepath.Dir(path)
	if err := checkDir(dir); err != nil {
		return err
	}
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".smart-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o640); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := chgrpAgent(tmp); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load: agent đọc kết quả. Chưa có file = chưa cài tác vụ SMART trên máy này.
func Load(path string) *protocol.SmartReport {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &protocol.SmartReport{Error: "chưa có kết quả SMART: máy chưa cài tác vụ scs-agent-smart (chạy bộ cài agent với --smart-only) hoặc tác vụ chưa chạy lần nào"}
	case err != nil:
		return &protocol.SmartReport{Error: "không đọc được kết quả SMART: " + err.Error()}
	case !fi.Mode().IsRegular() || fi.Size() > maxFile:
		return &protocol.SmartReport{Error: "file kết quả SMART không hợp lệ"}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return &protocol.SmartReport{Error: "không đọc được kết quả SMART: " + err.Error()}
	}
	var rep protocol.SmartReport
	if err := json.Unmarshal(b, &rep); err != nil {
		return &protocol.SmartReport{Error: "file kết quả SMART hỏng"}
	}
	if len(rep.Disks) > protocol.MaxSmart {
		rep.Disks = rep.Disks[:protocol.MaxSmart]
	}
	return &rep
}

package smart

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

// nvmeLog dựng log SMART NVMe 512 byte.
func nvmeLog(warn byte, tempK uint16, spare, used byte, cycles, hours, unsafe, media uint64) []byte {
	b := make([]byte, 512)
	b[0] = warn
	binary.LittleEndian.PutUint16(b[1:3], tempK)
	b[3], b[5] = spare, used
	binary.LittleEndian.PutUint64(b[112:], cycles)
	binary.LittleEndian.PutUint64(b[128:], hours)
	binary.LittleEndian.PutUint64(b[144:], unsafe)
	binary.LittleEndian.PutUint64(b[160:], media)
	return b
}

type attr struct {
	id      byte
	prefail bool
	value   byte
	raw     uint64
	thresh  byte
}

// ataTables dựng SMART READ DATA + READ THRESHOLDS.
func ataTables(attrs ...attr) ([]byte, []byte) {
	data, thr := make([]byte, 512), make([]byte, 512)
	for i, a := range attrs {
		o := 2 + 12*i
		data[o] = a.id
		if a.prefail {
			data[o+1] = 1
		}
		data[o+3], data[o+4] = a.value, a.value
		binary.LittleEndian.PutUint32(data[o+5:], uint32(a.raw))
		binary.LittleEndian.PutUint16(data[o+9:], uint16(a.raw>>32))
		thr[o], thr[o+1] = a.id, a.thresh
	}
	return data, thr
}

func TestParseNVMe(t *testing.T) {
	disk := readDiskWith(t, "nvme0n1", [2][]byte{}, nvmeLog(0, 318, 100, 7, 120, 5400, 9, 0))
	if !disk.OK || *disk.Passed != true || disk.TempC != 45 || *disk.PercentUsed != 7 || *disk.PowerOnHours != 5400 ||
		*disk.PowerCycles != 120 || *disk.UnsafeShutdowns != 9 || *disk.MediaErrors != 0 || *disk.AvailSpare != 100 {
		t.Fatalf("NVMe khỏe: %+v", disk)
	}
	bad := readDiskWith(t, "nvme1n1", [2][]byte{}, nvmeLog(0b0100+0b1000, 330, 4, 104, 1, 1, 0, 37))
	if *bad.Passed || len(bad.Failing) != 2 || !strings.Contains(strings.Join(bad.Failing, ","), "chỉ đọc") || *bad.MediaErrors != 37 || *bad.PercentUsed != 104 {
		t.Fatalf("NVMe có cảnh báo: %+v", bad)
	}
}

func TestParseATA(t *testing.T) {
	data, thr := ataTables(
		attr{id: 5, prefail: true, value: 100, raw: 12, thresh: 10},
		attr{id: 9, value: 90, raw: 21345, thresh: 0},
		attr{id: 12, value: 99, raw: 812},
		attr{id: 194, value: 64, raw: 36 | 20<<16 | 50<<32}, // nhiệt độ 36, kèm min/max ở các byte trên
		attr{id: 197, value: 100, raw: 3},
		attr{id: 198, value: 100, raw: 1},
		attr{id: 199, value: 200, raw: 41},
		attr{id: 231, value: 18}, // còn 18% tuổi thọ → đã dùng 82%
	)
	disk := readDiskWith(t, "sda", [2][]byte{data, thr}, nil)
	if !disk.OK || !*disk.Passed || *disk.Reallocated != 12 || *disk.PowerOnHours != 21345 || *disk.PowerCycles != 812 ||
		disk.TempC != 36 || *disk.Pending != 3 || *disk.Uncorrectable != 1 || *disk.CRCErrors != 41 ||
		disk.WearAttr != 231 || *disk.PercentUsed != 82 || disk.ReportedUncor != nil {
		t.Fatalf("SATA: %+v", disk)
	}
	// Thuộc tính pre-fail chạm ngưỡng → ổ báo hỏng; thuộc tính thường chạm ngưỡng chỉ ghi lại.
	data, thr = ataTables(attr{id: 5, prefail: true, value: 9, raw: 4000, thresh: 10}, attr{id: 190, value: 30, raw: 70, thresh: 40})
	f := readDiskWith(t, "sdb", [2][]byte{data, thr}, nil)
	if *f.Passed || len(f.Failing) != 2 || !strings.Contains(f.Failing[0], "sector đã thay (5)") || f.TempC != 70 {
		t.Fatalf("SATA sắp hỏng: %+v", f)
	}
	data, thr = ataTables(attr{id: 190, value: 30, raw: 45, thresh: 40})
	if g := readDiskWith(t, "sdc", [2][]byte{data, thr}, nil); !*g.Passed || len(g.Failing) != 1 {
		t.Fatalf("thuộc tính thường chạm ngưỡng không làm ổ báo hỏng: %+v", g)
	}
}

// readDiskWith chạy readDisk với /sys giả và ioctl giả.
func readDiskWith(t *testing.T, name string, ata [2][]byte, nvme []byte) protocol.SmartDisk {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sys", "block", name, "device"), 0o755)
	oldRoot, oldATA, oldNVMe := Root, readATA, readNVMe
	Root = root
	readATA = func(string) ([]byte, []byte, error) {
		if ata[0] == nil {
			return nil, nil, errors.New("không phải ATA")
		}
		return ata[0], ata[1], nil
	}
	readNVMe = func(string) ([]byte, error) { return nvme, nil }
	t.Cleanup(func() { Root, readATA, readNVMe = oldRoot, oldATA, oldNVMe })
	return readDisk(name)
}

// Read duyệt /sys/block: bỏ ổ ảo / ổ rời; ổ ảo của card RAID, ổ USB, ổ không trả lời ATA đều ghi lý do, không lỗi.
func TestReadAndNotes(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, files map[string]string) {
		for f, v := range files {
			p := filepath.Join(root, "sys", "block", name, filepath.FromSlash(f))
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(v), 0o644)
		}
	}
	mk("sda", map[string]string{"removable": "0", "device/vendor": "ATA", "device/model": "Generic SSD 240GB"})
	mk("sdb", map[string]string{"removable": "0", "device/vendor": "LSI", "device/model": "MR9361-8i"})
	mk("sdc", map[string]string{"removable": "1"})
	mk("sdd", map[string]string{"removable": "0", "device/vendor": "SEAGATE", "device/model": "ST600MM0208"})
	mk("nvme0n1", map[string]string{"removable": "0"})
	mk("loop0", map[string]string{"removable": "0"})
	oldRoot, oldATA, oldNVMe := Root, readATA, readNVMe
	t.Cleanup(func() { Root, readATA, readNVMe = oldRoot, oldATA, oldNVMe })
	Root = root
	data, thr := ataTables(attr{id: 9, value: 99, raw: 100})
	readATA = func(dev string) ([]byte, []byte, error) {
		if dev == "/dev/sdd" {
			return nil, nil, errors.New("Invalid argument")
		}
		return data, thr, nil
	}
	readNVMe = func(dev string) ([]byte, error) {
		if dev != "/dev/nvme0" {
			t.Fatalf("NVMe phải đọc qua bộ điều khiển /dev/nvme0, gọi %s", dev)
		}
		return nvmeLog(0, 310, 100, 1, 1, 1, 0, 0), nil
	}
	now := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	rep := Read(now)
	got := map[string]string{}
	for _, d := range rep.Disks {
		got[d.Name] = d.Note
		if (d.Name == "sda" || d.Name == "nvme0n1") && !d.OK {
			t.Fatalf("%s phải đọc được: %+v", d.Name, d)
		}
	}
	if len(rep.Disks) != 4 || !rep.At.Equal(now) {
		t.Fatalf("ổ: %v", got)
	}
	if !strings.Contains(got["sdb"], "card RAID") || !strings.Contains(got["sdd"], "SAS") {
		t.Fatalf("lý do không đọc được: %v", got)
	}
}

// Save / Load: ghi file tạm rồi đổi tên; Load chịu được thiếu file, file hỏng, file quá lớn, symlink.
func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "smart.json")
	if r := Load(p); r.Error == "" || !strings.Contains(r.Error, "chưa cài") {
		t.Fatalf("chưa có file: %+v", r)
	}
	rep := Read(time.Now()) // không có /sys/block giả: chỉ cần đi qua Save / Load
	rep.Error = ""
	if err := Save(p, rep); err != nil {
		t.Fatal(err)
	}
	if r := Load(p); r.Error != "" {
		t.Fatalf("đọc lại: %+v", r)
	}
	os.WriteFile(p, []byte("{hỏng"), 0o600)
	if r := Load(p); !strings.Contains(r.Error, "hỏng") {
		t.Fatalf("file hỏng: %+v", r)
	}
	os.WriteFile(p, make([]byte, maxFile+1), 0o600)
	if r := Load(p); !strings.Contains(r.Error, "không hợp lệ") {
		t.Fatalf("file quá lớn: %+v", r)
	}
}

//go:build linux

package smart

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"
)

// sg_io_hdr (scsi/sg.h): bố cục khớp C trên amd64 / arm64 nhờ căn lề tự nhiên của Go.
type sgIOHdr struct {
	interfaceID    int32
	dxferDirection int32
	cmdLen         uint8
	mxSbLen        uint8
	iovecCount     uint16
	dxferLen       uint32
	dxferp         uintptr
	cmdp           uintptr
	sbp            uintptr
	timeout        uint32
	flags          uint32
	packID         int32
	usrPtr         uintptr
	status         uint8
	maskedStatus   uint8
	msgStatus      uint8
	sbLenWr        uint8
	hostStatus     uint16
	driverStatus   uint16
	resid          int32
	duration       uint32
	info           uint32
}

const (
	sgIO            = 0x2285
	sgDxferFromDev  = -3
	nvmeIoctlAdmin  = 0xC0484E41 // _IOWR('N', 0x41, struct nvme_passthru_cmd), 72 byte
	ioTimeoutMillis = 10000
)

// nvme_passthru_cmd (linux/nvme_ioctl.h), 72 byte.
type nvmeAdminCmd struct {
	opcode      uint8
	flags       uint8
	rsvd1       uint16
	nsid        uint32
	cdw2        uint32
	cdw3        uint32
	metadata    uint64
	addr        uint64
	metadataLen uint32
	dataLen     uint32
	cdw10       uint32
	cdw11       uint32
	cdw12       uint32
	cdw13       uint32
	cdw14       uint32
	cdw15       uint32
	timeoutMs   uint32
	result      uint32
}

func init() {
	if unsafe.Sizeof(nvmeAdminCmd{}) != 72 || unsafe.Sizeof(sgIOHdr{}) != 88 {
		panic("smart: sai bố cục cấu trúc ioctl")
	}
}

// openRO mở thiết bị CHỈ ĐỌC, không theo symlink, không chờ (ổ quang / ổ treo không làm treo tác vụ).
func openRO(dev string) (*os.File, error) {
	return os.OpenFile(dev, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
}

// ataSmart gửi SMART (B0h) với feature cho trước qua ATA PASS-THROUGH(16), đọc 512 byte.
func ataSmart(f *os.File, feature byte) ([]byte, error) {
	buf, sense, cdb := heapBuf(512), heapBuf(32), heapBuf(16)
	copy(cdb, []byte{
		0x85,       // ATA PASS-THROUGH (16)
		4 << 1,     // giao thức PIO data-in
		0x0E,       // T_DIR = đọc, BYT_BLOK = khối, T_LENGTH = trường sector count
		0, feature, // features
		0, 1, // sector count = 1
		0, 0, // LBA low
		0, 0x4F, // LBA mid (khóa SMART)
		0, 0xC2, // LBA high (khóa SMART)
		0,    // device
		0xB0, // lệnh SMART
		0,
	})
	h := &sgIOHdr{interfaceID: 'S', dxferDirection: sgDxferFromDev, cmdLen: uint8(len(cdb)), mxSbLen: uint8(len(sense)),
		dxferLen: uint32(len(buf)), dxferp: uintptr(unsafe.Pointer(&buf[0])), cmdp: uintptr(unsafe.Pointer(&cdb[0])),
		sbp: uintptr(unsafe.Pointer(&sense[0])), timeout: ioTimeoutMillis}
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), sgIO, uintptr(unsafe.Pointer(h)))
	runtime.KeepAlive(buf)
	runtime.KeepAlive(sense)
	runtime.KeepAlive(cdb)
	if e != 0 {
		return nil, e
	}
	if h.status != 0 || h.hostStatus != 0 || h.driverStatus&^0x08 != 0 { // 0x08 = DRIVER_SENSE (thường kèm theo)
		return nil, fmt.Errorf("ổ trả trạng thái %#x/%#x/%#x", h.status, h.hostStatus, h.driverStatus)
	}
	if allZero(buf) {
		return nil, errors.New("ổ không trả dữ liệu SMART")
	}
	return buf, nil
}

func readATAOS(dev string) ([]byte, []byte, error) {
	f, err := openRO(dev)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	data, err := ataSmart(f, 0xD0) // SMART READ DATA
	if err != nil {
		return nil, nil, err
	}
	thr, _ := ataSmart(f, 0xD1) // SMART READ THRESHOLDS (lỗi thì bỏ qua: không xét ngưỡng)
	return data, thr, nil
}

func readNVMeOS(dev string) ([]byte, error) {
	f, err := openRO(dev)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := heapBuf(512)
	cmd := &nvmeAdminCmd{opcode: 0x02, nsid: 0xFFFFFFFF, addr: uint64(uintptr(unsafe.Pointer(&buf[0]))), dataLen: 512,
		cdw10: (512/4-1)<<16 | 0x02, timeoutMs: ioTimeoutMillis}
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), nvmeIoctlAdmin, uintptr(unsafe.Pointer(cmd)))
	runtime.KeepAlive(buf)
	if e != 0 {
		return nil, e
	}
	return buf, nil
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// checkDir: thư mục kết quả phải thuộc root, không ai khác ghi được.
func checkDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("thư mục %s phải là thư mục của root, người khác không ghi được (bộ cài tạo root:scs-agent 750)", dir)
	}
	return nil
}

// chgrpAgent cho nhóm scs-agent đọc file kết quả.
func chgrpAgent(p string) error {
	g, err := user.LookupGroup("scs-agent")
	if err != nil {
		return nil // máy thử không có nhóm: file chỉ root đọc
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return err
	}
	return os.Lchown(p, 0, gid)
}

// heapBuf: bộ đệm cho ioctl nằm trên heap (Go không dời đối tượng heap), vì địa chỉ được truyền dưới dạng
// uintptr bên trong cấu trúc ioctl — bộ đệm trên stack có thể bị dời khi stack lớn lên.
//
//go:noinline
func heapBuf(n int) []byte { return make([]byte, n) }

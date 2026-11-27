//go:build linux && (amd64 || arm64)

package lan

import (
	"encoding/binary"
	"errors"
	"math/bits"
	"net/netip"
	"runtime"
	"syscall"
	"unsafe"
)

// Đọc địa chỉ IPv4 của card mạng bằng ioctl trên socket AF_INET.
//
// Vì sao không dùng net.Interfaces: trên Linux nó dùng socket netlink (AF_NETLINK), mà dịch vụ scs-agent chạy với
// RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX (systemd chặn netlink) — net.Interfaces lỗi, agent tưởng máy
// không có card mạng nào (báo no_lan). Sửa file unit thì phải cài lại agent; ioctl trên AF_INET thì được phép.
//
// Bố cục struct (Linux, 64 bit): ifreq 40 byte = tên 16 byte + union 24 byte (sockaddr_in: family 2 byte theo thứ tự
// máy, cổng 2 byte, địa chỉ 4 byte); ifconf = int32 độ dài + 4 byte đệm + con trỏ.

const (
	siocGIFCONF    = 0x8912
	siocGIFFLAGS   = 0x8913
	siocGIFNETMASK = 0x891b
	ifreqSize      = 40
	ifNameSize     = 16
	iffUp          = 0x1
	iffLoopback    = 0x8
	maxIfreq       = 256
)

type ifconf struct {
	Len int32
	_   [4]byte
	Buf uintptr
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// ioctlPrefixes: các địa chỉ IPv4 (kèm mặt nạ) của card đang bật, trừ loopback.
func ioctlPrefixes() ([]netip.Prefix, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	buf := make([]byte, ifreqSize*maxIfreq)
	ic := ifconf{Len: int32(len(buf)), Buf: uintptr(unsafe.Pointer(&buf[0]))}
	err = ioctl(fd, siocGIFCONF, unsafe.Pointer(&ic))
	runtime.KeepAlive(buf)
	if err != nil {
		return nil, err
	}
	n := int(ic.Len) / ifreqSize
	if n < 0 || n > maxIfreq {
		return nil, errors.New("SIOCGIFCONF trả độ dài lạ")
	}
	var out []netip.Prefix
	for i := 0; i < n; i++ {
		req := buf[i*ifreqSize : (i+1)*ifreqSize]
		if binary.NativeEndian.Uint16(req[ifNameSize:]) != syscall.AF_INET {
			continue
		}
		ip := netip.AddrFrom4([4]byte(req[ifNameSize+4 : ifNameSize+8]))
		var r [ifreqSize]byte
		copy(r[:ifNameSize], req[:ifNameSize])
		if ioctl(fd, siocGIFFLAGS, unsafe.Pointer(&r[0])) != nil {
			continue
		}
		flags := binary.NativeEndian.Uint16(r[ifNameSize:])
		if flags&iffUp == 0 || flags&iffLoopback != 0 {
			continue
		}
		var m [ifreqSize]byte
		copy(m[:ifNameSize], req[:ifNameSize])
		if ioctl(fd, siocGIFNETMASK, unsafe.Pointer(&m[0])) != nil {
			continue
		}
		ones := bits.OnesCount32(binary.BigEndian.Uint32(m[ifNameSize+4 : ifNameSize+8]))
		if ones < 8 {
			continue // dải /0–/7 quá rộng: không coi là LAN
		}
		out = append(out, netip.PrefixFrom(ip, ones))
	}
	return out, nil
}

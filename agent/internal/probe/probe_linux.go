//go:build linux

package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"syscall"
	"time"
)

// ping: một gói ICMP echo qua socket ping không đặc quyền (SOCK_DGRAM, IPPROTO_ICMP) — không cần root hay
// CAP_NET_RAW; user agent phải nằm trong net.ipv4.ping_group_range (Ubuntu mặc định cho mọi nhóm).
func ping(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, syscall.IPPROTO_ICMP)
	if err != nil {
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EPROTONOSUPPORT) {
			return 0, fmt.Errorf("%w: không mở được socket ping (net.ipv4.ping_group_range): %v", ErrUnsupported, err)
		}
		return 0, err
	}
	defer syscall.Close(fd)
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < timeout {
		timeout = time.Until(dl)
	}
	tv := syscall.NsecToTimeval(max(timeout, time.Millisecond).Nanoseconds())
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return 0, err
	}
	seq := uint16(time.Now().UnixNano())
	pkt := make([]byte, 16)
	pkt[0] = 8 // echo request; id do kernel đặt theo socket
	binary.BigEndian.PutUint16(pkt[6:], seq)
	copy(pkt[8:], "scs-ping")
	binary.BigEndian.PutUint16(pkt[2:], checksum(pkt))
	to := &syscall.SockaddrInet4{Addr: ip.As4()}
	start := time.Now()
	if err := syscall.Sendto(fd, pkt, 0, to); err != nil {
		return 0, err
	}
	buf := make([]byte, 512)
	deadline := start.Add(timeout)
	for time.Now().Before(deadline) {
		n, from, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
				return 0, errors.New("timeout")
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return 0, err
		}
		sa, ok := from.(*syscall.SockaddrInet4)
		if !ok || sa.Addr != ip.As4() || n < 8 {
			continue
		}
		switch buf[0] {
		case 0: // echo reply
			if binary.BigEndian.Uint16(buf[6:]) == seq {
				return time.Since(start), nil
			}
		case 3: // destination unreachable
			return 0, errors.New("unreachable")
		}
	}
	return 0, errors.New("timeout")
}

func checksum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

// defaultGateway: router mặc định (IPv4) từ /proc/net/route.
func defaultGateway() (netip.Addr, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return netip.Addr{}, err
	}
	defer f.Close()
	return parseRoute(f)
}

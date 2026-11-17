package sysstat

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"strconv"
	"strings"
)

// Đọc /proc (Linux). Không có file (máy khác Linux) thì trả số 0 / nil, web hiện "—".

var procRoot = "/proc" // test đổi sang thư mục giả

func readFile(name string) []byte {
	b, err := os.ReadFile(procRoot + "/" + name)
	if err != nil || len(b) > 1<<20 {
		return nil
	}
	return b
}

func readUptime() int64 {
	f := strings.Fields(string(readFile("uptime")))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return int64(v)
}

func readLoad() []float64 {
	f := strings.Fields(string(readFile("loadavg")))
	if len(f) < 3 {
		return nil
	}
	out := make([]float64, 3)
	for i := range out {
		out[i], _ = strconv.ParseFloat(f[i], 64)
	}
	return out
}

// readMem: MemTotal, MemAvailable (byte).
func readMem() (total, avail uint64) {
	sc := bufio.NewScanner(bytes.NewReader(readFile("meminfo")))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return total, avail
}

// readRSS: RAM tiến trình scs-server đang dùng (VmRSS).
func readRSS() uint64 {
	sc := bufio.NewScanner(bytes.NewReader(readFile("self/status")))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				n, _ := strconv.ParseUint(f[0], 10, 64)
				return n * 1024
			}
		}
	}
	return 0
}

type cpuTimes struct{ total, idle uint64 }

func readCPU() (cpuTimes, bool) {
	line, _, _ := strings.Cut(string(readFile("stat")), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return cpuTimes{}, false
	}
	var t cpuTimes
	for i, s := range f[1:] {
		v, _ := strconv.ParseUint(s, 10, 64)
		if i >= 8 { // guest, guest_nice đã nằm trong user / nice
			break
		}
		t.total += v
		if i == 3 || i == 4 { // idle, iowait
			t.idle += v
		}
	}
	return t, true
}

// readNetDev: byte nhận / gửi của từng card mạng.
func readNetDev() map[string]netCounter {
	out := map[string]netCounter{}
	sc := bufio.NewScanner(bytes.NewReader(readFile("net/dev")))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		out[strings.TrimSpace(name)] = netCounter{rx, tx}
	}
	return out
}

// ifaceAddrs: IPv4 của card (IPv6 dài, trang Tổng quan chỉ cần IPv4).
func ifaceAddrs(name string) []string {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			out = append(out, n.String())
		}
	}
	return out
}

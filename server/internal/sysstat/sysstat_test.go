package sysstat

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeProc(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func netDev(rx, tx uint64) string {
	return "Inter-|   Receive |  Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets\n" +
		"    lo: 500 1 0 0 0 0 0 0 500 1 0 0 0 0 0 0\n" +
		"  eth0: " + itoa(rx) + " 10 0 0 0 0 0 0 " + itoa(tx) + " 10 0 0 0 0 0 0\n" +
		"veth12: 99 1 0 0 0 0 0 0 99 1 0 0 0 0 0 0\n"
}

func itoa(v uint64) string { return string(appendUint(nil, v)) }
func appendUint(b []byte, v uint64) []byte {
	if v >= 10 {
		b = appendUint(b, v/10)
	}
	return append(b, byte('0'+v%10))
}

func TestSampler(t *testing.T) {
	dir := t.TempDir()
	old := procRoot
	procRoot = dir
	defer func() { procRoot = old }()
	writeProc(t, dir, map[string]string{
		"uptime":      "3600.5 100.0\n",
		"loadavg":     "0.50 0.40 0.30 1/200 999\n",
		"meminfo":     "MemTotal:       8000000 kB\nMemFree:  100 kB\nMemAvailable:   6000000 kB\n",
		"self/status": "Name: scs\nVmRSS:\t   26000 kB\n",
		"stat":        "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 1 2 3\n",
		"net/dev":     netDev(1000, 2000),
	})
	act := &Activity{}
	s := New(act, "", true)

	// 10 giây sau: CPU bận 50%, mạng nhận 10 KB/s, 6 heartbeat, 1 lô 4 sự kiện.
	writeProc(t, dir, map[string]string{
		"stat":    "cpu  150 0 150 900 0 0 0 0 0 0\n",
		"net/dev": netDev(1000+100000, 2000+50000),
	})
	for i := 0; i < 6; i++ {
		act.Heartbeat()
	}
	act.Logs(4)
	act.IngestConns.Add(3)
	s.sample(time.Now().Add(Every))
	sn := s.Snapshot()

	if sn.Host.UptimeS != 3600 || len(sn.Host.Load) != 3 || sn.Host.Load[0] != 0.5 {
		t.Fatalf("uptime/load: %+v", sn.Host)
	}
	if sn.Host.MemTotal != 8000000*1024 || sn.Host.MemAvail != 6000000*1024 || sn.SCS.RSS != 26000*1024 {
		t.Fatalf("mem: %+v %d", sn.Host, sn.SCS.RSS)
	}
	if sn.Host.CPUPct == nil || *sn.Host.CPUPct != 50 {
		t.Fatalf("cpu: %v", sn.Host.CPUPct)
	}
	if len(sn.Net.Ifaces) != 1 || sn.Net.Ifaces[0].Name != "eth0" {
		t.Fatalf("chỉ giữ card thật (bỏ lo, veth): %+v", sn.Net.Ifaces)
	}
	e := sn.Net.Ifaces[0]
	if e.RxToday != 100000 || e.TxToday != 50000 || e.RxBps < 9000 || e.RxBps > 11000 {
		t.Fatalf("mạng: %+v", e)
	}
	if sn.Net.IngestConns != 3 || !sn.SCS.IngestHere {
		t.Fatalf("kết nối agent: %+v", sn.Net)
	}
	if sn.SCS.HeartbeatsMin < 35 || sn.SCS.HeartbeatsMin > 37 || sn.SCS.LogEventsMin < 23 || sn.SCS.LogEventsMin > 25 {
		t.Fatalf("mỗi phút: %+v", sn.SCS)
	}
	if sn.SCS.HeartbeatsTotal != 6 || sn.SCS.LogEventsTotal != 4 {
		t.Fatalf("tổng: %+v", sn.SCS)
	}

	// Bộ đếm card mạng quay về 0 (card khởi động lại): không ra số âm / số khổng lồ.
	writeProc(t, dir, map[string]string{"net/dev": netDev(10, 10)})
	s.sample(time.Now().Add(2 * Every))
	e = s.Snapshot().Net.Ifaces[0]
	if e.RxToday > 1000 || e.RxBps != 0 {
		t.Fatalf("bộ đếm quay về 0: %+v", e)
	}
}

func TestNoProc(t *testing.T) {
	old := procRoot
	procRoot = t.TempDir()
	defer func() { procRoot = old }()
	s := New(nil, "", false)
	sn := s.Snapshot()
	if sn.Host.CPUPct != nil || sn.Host.MemTotal != 0 || len(sn.Net.Ifaces) != 0 {
		t.Fatalf("không có /proc thì để trống: %+v", sn)
	}
	var a *Activity
	a.Heartbeat() // nil an toàn
	a.Logs(3)
}

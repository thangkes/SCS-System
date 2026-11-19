package sysinfo

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/common/protocol"
)

func TestParseLoadavgUptime(t *testing.T) {
	l, err := parseLoadavg("0.52 0.58 0.59 1/1234 5678\n")
	if err != nil || !reflect.DeepEqual(l, []float64{0.52, 0.58, 0.59}) {
		t.Fatalf("loadavg = %v, %v", l, err)
	}
	if _, err := parseLoadavg("abc"); err == nil {
		t.Error("loadavg sai phải lỗi")
	}
	if u, err := parseUptime("18230.52 70000.11"); err != nil || u != 18230 {
		t.Fatalf("uptime = %d, %v", u, err)
	}
}

func TestParseMeminfoOSRelease(t *testing.T) {
	m := parseMeminfo(strings.NewReader("MemTotal:        8041760 kB\nMemAvailable:    5123456 kB\nSwapTotal: 0 kB\nHugePages_Total: 0\n"))
	if m["MemTotal"] != 8041760 || m["MemAvailable"] != 5123456 || m["SwapTotal"] != 0 {
		t.Fatalf("meminfo = %v", m)
	}
	os := parseOSRelease(strings.NewReader("NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 22.04.4 LTS\"\nVERSION_ID=\"22.04\"\n"))
	if os != "Ubuntu 22.04.4 LTS" {
		t.Fatalf("os-release = %q", os)
	}
}

func TestParseDefaultRoute(t *testing.T) {
	route := `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
wlp2s0	00000000	0101A8C0	0003	0	0	600	00000000	0	0	0
enp1s0	00000000	0100A8C0	0003	0	0	100	00000000	0	0	0
enp1s0	0000A8C0	00000000	0001	0	0	100	00FFFFFF	0	0	0
`
	if got := parseDefaultRouteIface(strings.NewReader(route)); got != "enp1s0" {
		t.Fatalf("card mạng chính = %q, muốn enp1s0 (metric nhỏ nhất)", got)
	}
	if got := parseDefaultRouteIface(strings.NewReader("Iface\tDestination\n")); got != "" {
		t.Fatalf("không có tuyến mặc định phải rỗng, được %q", got)
	}
}

func TestParseMounts(t *testing.T) {
	mounts := `sysfs /sys sysfs rw 0 0
/dev/md0 / ext4 rw,relatime 0 0
tmpfs /run tmpfs rw 0 0
/dev/sda1 /boot/efi vfat ro,relatime 0 0
/dev/loop3 /snap/core20/2318 squashfs ro 0 0
/dev/md0 /var/lib/docker ext4 rw 0 0
/dev/sdb1 /mnt/du\040lieu ext4 rw 0 0
`
	got := parseMounts(strings.NewReader(mounts))
	want := []mount{
		{"/dev/md0", "/", "ext4", false},
		{"/dev/sda1", "/boot/efi", "vfat", true}, // mount chỉ đọc
		{"/dev/sdb1", "/mnt/du lieu", "ext4", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mounts = %+v", got)
	}
}

func TestParseMdstat(t *testing.T) {
	mdstat := `Personalities : [raid1] [linear]
md0 : active raid1 sdb1[1] sda1[0]
      976630464 blocks super 1.2 [2/2] [UU]
      bitmap: 1/8 pages [4KB], 65536KB chunk

md1 : active raid1 sda2[0] sdb2[1](F)
      1234 blocks super 1.2 [2/1] [U_]
      [==>..................]  recovery = 12.6% (12345/97654) finish=10.0min speed=100K/sec

md2 : inactive sdc1[0](S)
      1000 blocks super 1.2

unused devices: <none>
`
	got := parseMdstat(strings.NewReader(mdstat))
	want := []protocol.RAIDStatus{
		{Name: "md0", Level: "raid1", Disks: 2, Active: 2},
		{Name: "md1", Level: "raid1", Disks: 2, Active: 1, Degraded: true, Sync: "recovery 12.6%"},
		{Name: "md2", Level: "", Degraded: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mdstat =\n%+v\nmuốn\n%+v", got, want)
	}
	if got := parseMdstat(strings.NewReader("Personalities :\nunused devices: <none>\n")); len(got) != 0 {
		t.Fatalf("không có RAID phải rỗng: %+v", got)
	}
}

func TestParseProps(t *testing.T) {
	p := parseProps("ActiveState=failed\nSubState=failed\n")
	if p["ActiveState"] != "failed" || p["SubState"] != "failed" {
		t.Fatalf("props = %v", p)
	}
}

// Đọc từ một gốc hệ thống file mẫu (như /proc, /sys của máy siêu thị) và lệnh giả.
func TestCollectFromFakeRoot(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(s), 0o644)
	}
	write("etc/machine-id", "0123456789abcdef0123456789abcdef\n")
	write("sys/class/dmi/id/board_serial", "BSN12345\n")
	write("sys/class/dmi/id/product_serial", "To be filled by O.E.M.\n")
	write("proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\nenp1s0\t00000000\t0100A8C0\t0003\t0\t0\t100\t00000000\n")
	write("sys/class/net/enp1s0/address", "AA:BB:CC:00:11:22\n")
	write("etc/os-release", "PRETTY_NAME=\"Ubuntu 22.04.4 LTS\"\n")
	write("proc/sys/kernel/osrelease", "5.15.0-105-generic\n")
	write("proc/loadavg", "0.10 0.20 0.30 1/100 200\n")
	write("proc/meminfo", "MemTotal: 8000000 kB\nMemAvailable: 6000000 kB\n")
	write("proc/self/mounts", "")
	write("proc/mdstat", "md0 : active raid1 sdb1[1] sda1[0]\n      100 blocks [2/2] [UU]\n")
	write("proc/uptime", "3600.5 100.0\n")
	// Ổ vật lý: sda (HDD SATA, có 3 lỗi I/O), nvme0n1 (SSD, không có bộ đếm lỗi), loop0 / sr0 / ổ USB bị bỏ.
	write("sys/block/sda/size", "1953525168\n")
	write("sys/block/sda/removable", "0\n")
	write("sys/block/sda/queue/rotational", "1\n")
	write("sys/block/sda/device/model", "WDC WD10EZEX-08W\x01  \n")
	write("sys/block/sda/device/ioerr_cnt", "0x3\n")
	write("sys/block/nvme0n1/size", "500118192\n")
	write("sys/block/nvme0n1/removable", "0\n")
	write("sys/block/nvme0n1/queue/rotational", "0\n")
	write("sys/block/nvme0n1/device/model", "Samsung SSD 980 500GB\n")
	write("sys/block/loop0/size", "100\n")
	write("sys/block/sr0/size", "100\n")
	write("sys/block/sdc/size", "100\n")
	write("sys/block/sdc/removable", "1\n")
	write("sys/class/thermal/thermal_zone0/temp", "48000\n")
	write("sys/class/thermal/thermal_zone1/temp", "61500\n")

	oldRoot, oldRun := Root, run
	Root = root
	var calls []string
	run = func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "timedatectl" {
			return "NTPSynchronized=yes\nNTP=yes\n", nil
		}
		return "ActiveState=active\nSubState=running\n", nil
	}
	t.Cleanup(func() { Root, run = oldRoot, oldRun })

	c, errs := Components()
	if len(errs) != 0 || c.MachineID == "" || c.BoardSerial != "BSN12345" || c.PrimaryMAC != "AA:BB:CC:00:11:22" {
		t.Fatalf("components = %+v, lỗi %v", c, errs)
	}
	if c.Normalize().SystemSerial != "" {
		t.Error("serial mặc định của hãng phải coi như không có")
	}
	if u, err := Uptime(); err != nil || u != 3600 {
		t.Fatalf("uptime = %d, %v", u, err)
	}

	st := Status([]string{"pos.service"})
	if st.OS != "Ubuntu 22.04.4 LTS" || st.Kernel != "5.15.0-105-generic" || st.MemTotalKB != 8000000 ||
		len(st.Load) != 3 || len(st.RAID) != 1 || st.RAID[0].Degraded || !st.TimeSync.Synced ||
		len(st.Services) != 1 || st.Services[0].Active != "active" {
		t.Fatalf("status = %+v", st)
	}
	if st.BootUptimeS != 3600 || st.TempC != 61 || len(st.Block) != 2 {
		t.Fatalf("uptime / nhiệt độ / ổ vật lý: %d %d %+v", st.BootUptimeS, st.TempC, st.Block)
	}
	for _, b := range st.Block {
		switch b.Name {
		case "sda":
			if b.SSD || !b.IOErrKnown || b.IOErrors != 3 || b.SizeBytes != 1953525168*512 || b.Model != "WDC WD10EZEX-08W" {
				t.Fatalf("sda: %+v", b)
			}
		case "nvme0n1":
			if !b.SSD || b.IOErrKnown || b.Model != "Samsung SSD 980 500GB" {
				t.Fatalf("nvme: %+v", b)
			}
		default:
			t.Fatalf("không được có %s", b.Name)
		}
	}
	if want := "systemctl show -p ActiveState -p SubState pos.service"; calls[len(calls)-1] != want {
		t.Fatalf("lệnh systemctl = %q", calls[len(calls)-1])
	}

	// Thiếu quyền đọc serial: vẫn ra kết quả, kèm lý do.
	os.Remove(filepath.Join(root, "sys/class/dmi/id/board_serial"))
	if _, errs := Components(); len(errs) != 1 || !strings.Contains(errs[0], "serial mainboard") {
		t.Fatalf("thiếu serial phải báo đúng phần thiếu: %v", errs)
	}
}

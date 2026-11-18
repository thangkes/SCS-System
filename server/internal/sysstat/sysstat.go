// Package sysstat thu số liệu của máy server và hoạt động SCS cho trang Tổng quan (chỉ xem):
// CPU, RAM, ổ đĩa, tốc độ và lưu lượng mạng, heartbeat / sự kiện mỗi phút, kết nối agent đang mở.
//
// Đọc /proc mỗi 10 giây trong RAM, không ghi DB, không gọi lệnh ngoài. Ngoài Linux các số của máy để
// trống (web hiện "—"); số hoạt động SCS vẫn có.
package sysstat

import (
	"context"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Every: nhịp lấy mẫu.
const Every = 10 * time.Second

// Activity: bộ đếm do cổng agent tăng (an toàn khi gọi song song, nil thì bỏ qua).
type Activity struct {
	Heartbeats  atomic.Int64
	LogBatches  atomic.Int64
	LogEvents   atomic.Int64
	IngestConns atomic.Int64 // kết nối agent đang mở trên cổng 6222
}

func (a *Activity) Heartbeat() {
	if a != nil {
		a.Heartbeats.Add(1)
	}
}

func (a *Activity) Logs(events int) {
	if a != nil {
		a.LogBatches.Add(1)
		a.LogEvents.Add(int64(events))
	}
}

// Iface: một card mạng.
type Iface struct {
	Name    string   `json:"name"`
	Addrs   []string `json:"addrs"`
	RxBps   float64  `json:"rx_bps"` // byte/giây trong nhịp lấy mẫu gần nhất
	TxBps   float64  `json:"tx_bps"`
	RxToday uint64   `json:"rx_today"` // byte từ 0 giờ hôm nay (hoặc từ lúc server chạy nếu chạy sau 0 giờ)
	TxToday uint64   `json:"tx_today"`
}

// Snapshot: số liệu trả cho web.
type Snapshot struct {
	SampledAt time.Time `json:"sampled_at"`
	Host      struct {
		Name      string    `json:"name"`
		Linux     bool      `json:"linux"`
		UptimeS   int64     `json:"uptime_s,omitempty"`
		CPUs      int       `json:"cpus"`
		CPUPct    *float64  `json:"cpu_pct,omitempty"`
		Load      []float64 `json:"load,omitempty"`
		MemTotal  uint64    `json:"mem_total,omitempty"`
		MemAvail  uint64    `json:"mem_avail,omitempty"`
		DiskPath  string    `json:"disk_path,omitempty"`
		DiskTotal uint64    `json:"disk_total,omitempty"`
		DiskFree  uint64    `json:"disk_free,omitempty"`
	} `json:"host"`
	Net struct {
		Ifaces      []Iface   `json:"ifaces"`
		TodaySince  time.Time `json:"today_since,omitzero"`
		IngestConns int64     `json:"ingest_conns"`
	} `json:"net"`
	SCS struct {
		IngestHere      bool      `json:"ingest_here"` // cổng agent chạy trong tiến trình này (lệnh all)
		Started         time.Time `json:"started"`
		RSS             uint64    `json:"rss,omitempty"`
		Goroutines      int       `json:"goroutines"`
		HeartbeatsMin   float64   `json:"heartbeats_min"`
		LogEventsMin    float64   `json:"log_events_min"`
		LogBatchesMin   float64   `json:"log_batches_min"`
		HeartbeatsTotal int64     `json:"heartbeats_total"`
		LogEventsTotal  int64     `json:"log_events_total"`
	} `json:"scs"`
}

type actSample struct {
	at               time.Time
	hb, batches, evs int64
}

type netCounter struct{ rx, tx uint64 }

// Sampler lấy mẫu định kỳ; Snapshot đọc bản mới nhất.
type Sampler struct {
	Act        *Activity
	DiskPath   string // thư mục dữ liệu (đo dung lượng ổ chứa nó)
	IngestHere bool

	start time.Time
	mu    sync.Mutex
	snap  Snapshot
	acts  []actSample // ~70 giây gần nhất, để tính số mỗi phút
	cpu   cpuTimes
	net   map[string]netCounter
	netAt time.Time
	day   string
	base  map[string]netCounter
	since time.Time
	acc   minuteAcc
}

// minuteAcc: cộng dồn các mẫu 10 giây cho tới khi TakeMinute lấy ra (ghi lịch sử mỗi phút).
type minuteAcc struct {
	cpuSum, cpuMax   float64
	cpuN, n          int
	rx, tx           uint64
	rxPeak, txPeak   float64
	connsMax         int64
	hbStart, evStart int64
	started          bool
}

// Minute: số liệu một phút để lưu lịch sử. CPUAvg < 0 = không đo được.
type Minute struct {
	At                  time.Time
	Host                string
	Samples             int
	CPUAvg, CPUMax      float64
	Load1               float64
	MemUsed, MemTotal   uint64
	DiskUsed, DiskTotal uint64
	RxBytes, TxBytes    uint64  // tổng mọi card (trừ card ảo) trong phút
	RxPeak, TxPeak      float64 // byte/giây cao nhất giữa hai mẫu 10 giây
	Heartbeats          int64
	LogEvents           int64
	ConnsMax            int64
	RSS                 uint64
}

// TakeMinute trả số liệu cộng dồn từ lần gọi trước và bắt đầu cộng lại. Samples = 0: chưa có mẫu nào.
func (s *Sampler) TakeMinute(at time.Time) Minute {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, sn := s.acc, s.snap
	hb, ev := s.Act.Heartbeats.Load(), s.Act.LogEvents.Load()
	m := Minute{At: at, Host: sn.Host.Name, Samples: a.n, CPUAvg: -1, CPUMax: -1, MemTotal: sn.Host.MemTotal,
		DiskTotal: sn.Host.DiskTotal, RxBytes: a.rx, TxBytes: a.tx, RxPeak: a.rxPeak, TxPeak: a.txPeak,
		ConnsMax: a.connsMax, RSS: sn.SCS.RSS}
	if a.cpuN > 0 {
		m.CPUAvg, m.CPUMax = a.cpuSum/float64(a.cpuN), a.cpuMax
	}
	if len(sn.Host.Load) > 0 {
		m.Load1 = sn.Host.Load[0]
	}
	if sn.Host.MemTotal > 0 {
		m.MemUsed = sn.Host.MemTotal - sn.Host.MemAvail
	}
	if sn.Host.DiskTotal > 0 {
		m.DiskUsed = sn.Host.DiskTotal - sn.Host.DiskFree
	}
	if a.started {
		m.Heartbeats, m.LogEvents = hb-a.hbStart, ev-a.evStart
	}
	s.acc = minuteAcc{hbStart: hb, evStart: ev, started: true}
	return m
}

// New tạo bộ lấy mẫu và lấy mẫu đầu tiên ngay.
func New(act *Activity, diskPath string, ingestHere bool) *Sampler {
	if act == nil {
		act = &Activity{}
	}
	s := &Sampler{Act: act, DiskPath: diskPath, IngestHere: ingestHere, start: time.Now(),
		net: map[string]netCounter{}, base: map[string]netCounter{}}
	s.sample(time.Now())
	return s
}

// Run lấy mẫu mỗi Every tới khi ctx bị hủy.
func (s *Sampler) Run(ctx context.Context) error {
	t := time.NewTicker(Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			s.sample(now)
		}
	}
}

// Snapshot trả bản số liệu mới nhất.
func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.snap
	out.Net.Ifaces = append([]Iface(nil), s.snap.Net.Ifaces...)
	out.Net.IngestConns = s.Act.IngestConns.Load()
	return out
}

// skipIface: card ảo của Docker / máy ảo, loopback.
func skipIface(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range []string{"veth", "docker", "br-", "virbr", "vnet", "tun", "cni", "flannel"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func (s *Sampler) sample(now time.Time) {
	var sn Snapshot
	sn.SampledAt = now
	sn.Host.Name, _ = os.Hostname()
	sn.Host.Linux = runtime.GOOS == "linux"
	sn.Host.CPUs = runtime.NumCPU()
	sn.Host.UptimeS = readUptime()
	sn.Host.Load = readLoad()
	sn.Host.MemTotal, sn.Host.MemAvail = readMem()
	if s.DiskPath != "" {
		sn.Host.DiskPath, sn.Host.DiskTotal, sn.Host.DiskFree = diskUsage(s.DiskPath)
	}
	sn.SCS.IngestHere = s.IngestHere
	sn.SCS.Started = s.start
	sn.SCS.RSS = readRSS()
	sn.SCS.Goroutines = runtime.NumGoroutine()
	hb, batches, evs := s.Act.Heartbeats.Load(), s.Act.LogBatches.Load(), s.Act.LogEvents.Load()
	sn.SCS.HeartbeatsTotal, sn.SCS.LogEventsTotal = hb, evs
	cpu, cpuOK := readCPU()
	counters := readNetDev()

	s.mu.Lock()
	defer s.mu.Unlock()
	// CPU: phần trăm thời gian bận giữa hai lần lấy mẫu.
	if cpuOK {
		if s.cpu.total > 0 && cpu.total > s.cpu.total {
			pct := 100 * float64((cpu.total-cpu.idle)-(s.cpu.total-s.cpu.idle)) / float64(cpu.total-s.cpu.total)
			sn.Host.CPUPct = &pct
			s.acc.cpuSum += pct
			s.acc.cpuN++
			s.acc.cpuMax = max(s.acc.cpuMax, pct)
		}
		s.cpu = cpu
	}
	// Hoạt động mỗi phút: so với mẫu cách ~60 giây.
	s.acts = append(s.acts, actSample{now, hb, batches, evs})
	for len(s.acts) > 1 && now.Sub(s.acts[1].at) >= time.Minute {
		s.acts = s.acts[1:]
	}
	if old := s.acts[0]; now.Sub(old.at) >= Every {
		f := float64(time.Minute) / float64(now.Sub(old.at))
		sn.SCS.HeartbeatsMin = float64(hb-old.hb) * f
		sn.SCS.LogBatchesMin = float64(batches-old.batches) * f
		sn.SCS.LogEventsMin = float64(evs-old.evs) * f
	}
	// Mạng: tốc độ theo nhịp, lưu lượng từ đầu ngày (giờ máy server).
	day := now.Format("2006-01-02")
	if day != s.day {
		s.day, s.base, s.since = day, map[string]netCounter{}, now
		if s.netAt.IsZero() { // lần đầu: chạy sau 0 giờ, chỉ đếm từ lúc này
			s.since = now
		} else {
			s.since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		}
		for k, v := range s.net { // mốc đầu ngày = mẫu cuối của hôm trước
			s.base[k] = v
		}
	}
	dt := now.Sub(s.netAt).Seconds()
	if !s.acc.started {
		s.acc.hbStart, s.acc.evStart, s.acc.started = hb, evs, true
	}
	s.acc.n++
	s.acc.connsMax = max(s.acc.connsMax, s.Act.IngestConns.Load())
	var rxRate, txRate float64
	names := make([]string, 0, len(counters))
	for n := range counters {
		if !skipIface(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		c := counters[n]
		prev, had := s.net[n]
		b, hasBase := s.base[n]
		if !hasBase || c.rx < b.rx || c.tx < b.tx { // card mới / bộ đếm quay về 0
			b = c
			if had && c.rx >= prev.rx && c.tx >= prev.tx {
				b = prev
			}
			s.base[n] = b
		}
		ifc := Iface{Name: n, Addrs: ifaceAddrs(n), RxToday: c.rx - b.rx, TxToday: c.tx - b.tx}
		if had && dt > 0 && c.rx >= prev.rx && c.tx >= prev.tx {
			ifc.RxBps, ifc.TxBps = float64(c.rx-prev.rx)/dt, float64(c.tx-prev.tx)/dt
			s.acc.rx += c.rx - prev.rx
			s.acc.tx += c.tx - prev.tx
			rxRate += ifc.RxBps
			txRate += ifc.TxBps
		}
		// Card không có IP và chưa từng có lưu lượng thì bỏ (card thừa, đang tắt).
		if len(ifc.Addrs) == 0 && c.rx == 0 && c.tx == 0 {
			continue
		}
		sn.Net.Ifaces = append(sn.Net.Ifaces, ifc)
	}
	s.acc.rxPeak, s.acc.txPeak = max(s.acc.rxPeak, rxRate), max(s.acc.txPeak, txRate)
	s.net, s.netAt = counters, now
	sn.Net.TodaySince = s.since
	s.snap = sn
}

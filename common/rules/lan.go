package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kiểm kê thiết bị LAN (schema 4): bộ rule có thể kèm một sơ đồ LAN — danh sách thiết bị cố định trong mạng
// của siêu thị (cân, máy thu ngân, máy in, đầu ghi...). Mọi máy có agent kiểm cả danh sách sau mỗi EveryHB
// heartbeat thành công, rồi gửi kết quả dạng bitmap kèm heartbeat kế tiếp. Không phải rule: không khớp,
// không sinh sự kiện, không cảnh báo — chỉ để server đếm thiết bị có mặt (trang "Thiết bị LAN").
//
// Thứ tự Targets là thứ tự bit trong bitmap. Server và agent hiểu bitmap giống nhau nhờ ListV (băm danh
// sách đích); sửa thêm thiết bị thì chỉ thêm vào cuối để lịch sử các vị trí cũ không đổi nghĩa.
//
// Giới hạn: IPv4 nội bộ cố định (không .0 / .255), tối đa MaxLANTargets đích, nhịp ≥ MinLANEveryHB heartbeat.
// Agent chỉ kiểm đích nằm cùng dải mạng với một card mạng của chính máy (không thể dùng để quét mạng khác);
// server chỉ ký sơ đồ có đích nằm trong dải admin cho phép (SCS_LAN_ALLOW, mặc định trống = không ký).

// LANSchema: schema bộ rule tối thiểu để có sơ đồ LAN. LANRandomSchema: có every_hb_max (nhịp ngẫu nhiên) — agent
// bản schema 4 đọc JSON chặt (trường lạ là bỏ cả bộ) nên trường mới cần schema riêng để server không phát cho nó.
const (
	LANSchema       = 4
	LANRandomSchema = 5
)

// Giới hạn sơ đồ LAN.
const (
	MaxLANTargets     = 48
	MinLANEveryHB     = 3
	MaxLANEveryHB     = 288
	DefaultLANEveryHB = 7
	MaxLANRetry       = 2
	DefaultLANRetry   = 1
	MaxLANLabel       = 48
)

// LANTypes: loại thiết bị (hiện trên web, lọc theo loại). "tn" là máy thu ngân — server đối chiếu với máy có agent.
var LANTypes = map[string]string{
	"can":    "Cân",
	"tn":     "Máy thu ngân",
	"may_in": "Máy in",
	"dvr":    "Đầu ghi",
	"mang":   "Thiết bị mạng",
	"khac":   "Khác",
}

// LAN: sơ đồ thiết bị LAN của một bộ rule.
type LAN struct {
	EveryHB int `json:"every_hb,omitempty"` // kiểm sau bấy nhiêu heartbeat thành công (mặc định 7)
	// EveryHBMax: có thì sau mỗi lượt agent bốc ngẫu nhiên số heartbeat tới lượt sau trong [every_hb, every_hb_max]
	// (cần schema 5). 0 = luôn every_hb.
	EveryHBMax int         `json:"every_hb_max,omitempty"`
	Retry      int         `json:"retry,omitempty"`      // thử lại khi không đáp (mặc định 1; -1 = không thử lại)
	TimeoutMs  int         `json:"timeout_ms,omitempty"` // chờ mỗi lần (mặc định 1500, tối đa 5000)
	Targets    []LANTarget `json:"targets"`
}

// LANTarget: một thiết bị.
type LANTarget struct {
	IP    string `json:"ip"`
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Kind  string `json:"kind,omitempty"` // icmp (mặc định) | tcp
	Port  int    `json:"port,omitempty"` // tcp: cổng phải mở được
}

var lanLabelBad = regexp.MustCompile(`[<>"'&\\]`)

// Every / Tries / Timeout: giá trị dùng khi chạy (đã áp mặc định).
func (l *LAN) Every() int {
	if l.EveryHB == 0 {
		return DefaultLANEveryHB
	}
	return l.EveryHB
}

// EveryMax: cận trên của nhịp (bằng Every khi không khai every_hb_max).
func (l *LAN) EveryMax() int {
	if l.EveryHBMax < l.Every() {
		return l.Every()
	}
	return l.EveryHBMax
}

// NextEvery: số heartbeat tới lượt kiểm sau — ngẫu nhiên đều trong [Every, EveryMax]. intn(n) trả số trong [0, n).
func (l *LAN) NextEvery(intn func(int) int) int {
	lo, hi := l.Every(), l.EveryMax()
	if hi <= lo || intn == nil {
		return lo
	}
	return lo + intn(hi-lo+1)
}

func (l *LAN) Tries() int {
	switch {
	case l.Retry < 0:
		return 1
	case l.Retry == 0:
		return 1 + DefaultLANRetry
	}
	return 1 + min(l.Retry, MaxLANRetry)
}

func (l *LAN) Timeout() int {
	if l.TimeoutMs == 0 {
		return DefaultProbeTimeMs
	}
	return l.TimeoutMs
}

// KindOf: cách kiểm thực tế của đích (mặc định icmp).
func (t LANTarget) KindOf() string {
	if t.Kind == "" {
		return "icmp"
	}
	return t.Kind
}

// Key: khóa ổn định của đích (không phụ thuộc vị trí): "10.0.0.5" hoặc "10.0.0.5:9100".
func (t LANTarget) Key() string {
	if t.KindOf() == "tcp" {
		return t.IP + ":" + strconv.Itoa(t.Port)
	}
	return t.IP
}

// Addr: IP đã kiểm hợp lệ (gọi sau Validate).
func (t LANTarget) Addr() netip.Addr {
	a, _ := netip.ParseAddr(t.IP)
	return a
}

// ListV: dấu của danh sách đích (thứ tự + ip + kiểu + cổng; không gồm nhãn / loại). Agent gửi kèm bitmap để
// server biết bit nào ứng với đích nào.
func (l *LAN) ListV() string {
	h := sha256.New()
	for i, t := range l.Targets {
		fmt.Fprintf(h, "%d|%s|%s\n", i, t.KindOf(), t.Key())
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// LANAddrAllowed: IP được phép đưa vào sơ đồ — IPv4 nội bộ, không phải địa chỉ mạng / broadcast thường gặp.
func LANAddrAllowed(ip netip.Addr) bool {
	if !ProbeAllowed(ip) {
		return false
	}
	last := ip.As4()[3]
	return last != 0 && last != 255
}

func (l *LAN) validate() []string {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if l.EveryHB != 0 && (l.EveryHB < MinLANEveryHB || l.EveryHB > MaxLANEveryHB) {
		add("lan.every_hb: từ %d tới %d heartbeat", MinLANEveryHB, MaxLANEveryHB)
	}
	if l.EveryHBMax != 0 && (l.EveryHBMax < l.Every() || l.EveryHBMax > MaxLANEveryHB) {
		add("lan.every_hb_max: từ every_hb (%d) tới %d heartbeat", l.Every(), MaxLANEveryHB)
	}
	if l.Retry < -1 || l.Retry > MaxLANRetry {
		add("lan.retry: từ -1 (không thử lại) tới %d", MaxLANRetry)
	}
	if l.TimeoutMs != 0 && (l.TimeoutMs < 100 || l.TimeoutMs > MaxProbeTimeoutMs) {
		add("lan.timeout_ms: từ 100 tới %d", MaxProbeTimeoutMs)
	}
	if len(l.Targets) == 0 || len(l.Targets) > MaxLANTargets {
		add("lan.targets: từ 1 tới %d thiết bị", MaxLANTargets)
	}
	seen := map[string]bool{}
	for i, t := range l.Targets {
		where := fmt.Sprintf("lan.targets #%d", i+1)
		ip, err := netip.ParseAddr(t.IP)
		if err != nil || ip.String() != t.IP || strings.Contains(t.IP, "/") {
			add("%s %q: chỉ một IPv4 cố định (không dải IP, không tên miền)", where, t.IP)
			continue
		}
		if !LANAddrAllowed(ip) {
			add("%s %q: chỉ IPv4 nội bộ (10/8, 172.16/12, 192.168/16), không .0 / .255", where, t.IP)
		}
		if _, ok := LANTypes[t.Type]; !ok {
			add("%s: type %q không hợp lệ (%s)", where, t.Type, lanTypeList())
		}
		if utf8.RuneCountInString(t.Label) > MaxLANLabel || !printable(t.Label) || lanLabelBad.MatchString(t.Label) {
			add("%s: label tối đa %d ký tự, không có ký tự điều khiển hay < > \" ' & \\", where, MaxLANLabel)
		}
		switch t.KindOf() {
		case "icmp":
			if t.Port != 0 {
				add("%s: port chỉ dùng với kind tcp", where)
			}
		case "tcp":
			if t.Port < 1 || t.Port > 65535 {
				add("%s: kind tcp cần port 1–65535", where)
			}
		default:
			add("%s: kind %q — icmp hoặc tcp", where, t.Kind)
		}
		if k := t.Key(); seen[k] {
			add("%s %q: bị trùng", where, k)
		} else {
			seen[k] = true
		}
	}
	return errs
}

func lanTypeList() string {
	return "can, tn, may_in, dvr, mang, khac"
}

// OutsidePrefixes: các đích KHÔNG nằm trong dải nào được phép (server kiểm lúc ký, theo SCS_LAN_ALLOW).
func (l *LAN) OutsidePrefixes(allow []netip.Prefix) []string {
	var out []string
	for _, t := range l.Targets {
		ip := t.Addr()
		ok := false
		for _, p := range allow {
			if p.Contains(ip) {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, t.IP)
		}
	}
	return out
}

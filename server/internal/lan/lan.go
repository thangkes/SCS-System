// Package lan: server tổng hợp kết quả kiểm kê thiết bị LAN mà mọi máy agent của siêu thị gửi kèm heartbeat
// (trang web "Thiết bị LAN" — chỉ để tham khảo, không cảnh báo).
//
// Mỗi máy gửi bitmap theo sơ đồ LAN của bộ rule đã ký (ListV). Server giữ:
//   - Reporter: báo cáo gần nhất của từng máy (1 dòng / máy);
//   - Site: trạng thái gộp của siêu thị (1 dòng / siêu thị): mỗi thiết bị (theo khóa ip / ip:port, không theo vị
//     trí bit) có lần thấy đầu / gần nhất, thấy bởi máy nào, trạng thái đã ghi lịch sử;
//   - Change: lịch sử đổi trạng thái (giữ 10 ngày, tối đa MaxChangesPerDay lần / thiết bị / ngày).
//
// Quy tắc (logic thuần trong file này, phần lưu PostgreSQL ở package store):
//   - online: có ít nhất một máy thấy trong Window (5 giờ);
//   - absent (vắng): từng thấy, nay quá Window không máy nào thấy; gone (lâu không thấy): quá GoneAfter (30 ngày);
//   - never (chưa từng thấy): coi như siêu thị không có thiết bị này, không tính là thiếu;
//   - unknown (không rõ, chỉ khi hiển thị): không máy nào của siêu thị báo trong Window;
//   - lệch: một máy liên tục (≥ LechRounds lượt) không thấy thiết bị máy khác vừa thấy → nghi đường mạng của máy đó;
//   - máy báo bất thường: thấy ít hơn một nửa so với trung vị các máy khác → không tính lệch cho máy đó.
package lan

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/common/rules"
)

// Ngưỡng tổng hợp.
const (
	Window           = 5 * time.Hour
	GoneAfter        = 30 * 24 * time.Hour
	Retention        = 10 * 24 * time.Hour
	MaxChangesPerDay = 6
	LechRounds       = 2
	// SaveEvery: dòng siêu thị / dòng máy không đổi gì đáng kể thì ghi lại tối đa mỗi giờ một lần (giảm ghi DB).
	SaveEvery = time.Hour
	// LechFresh: máy khác thấy thiết bị trong khoảng này thì "máy này không thấy" mới tính là lệch.
	LechFresh = 3 * time.Hour
)

// Trạng thái thiết bị.
const (
	StOnline  = "online"
	StAbsent  = "absent"
	StGone    = "gone"
	StNever   = "never"
	StUnknown = "unknown"
)

// ParseAllow đọc SCS_LAN_ALLOW: các dải IPv4 nội bộ, cách nhau bởi dấu phẩy / khoảng trắng. Trống = không cho ký
// sơ đồ LAN nào.
func ParseAllow(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\t' }) {
		p, err := netip.ParsePrefix(f)
		if err != nil || !p.Addr().Is4() || !p.Addr().IsPrivate() || p.Bits() < 8 || p.Masked() != p {
			return nil, fmt.Errorf("SCS_LAN_ALLOW %q: cần dải IPv4 nội bộ dạng 192.168.1.0/24 (độ dài ≥ /8, đúng địa chỉ mạng)", f)
		}
		out = append(out, p)
	}
	return out, nil
}

// SignCheck: kiểm trước khi ký — bộ có sơ đồ LAN thì mọi đích phải nằm trong allow.
func SignCheck(allow []netip.Prefix) func(*rules.RuleSet) error {
	return func(s *rules.RuleSet) error {
		if s.LAN == nil {
			return nil
		}
		if len(allow) == 0 {
			return errors.New("bộ rule có sơ đồ LAN nhưng server chưa khai dải được phép (SCS_LAN_ALLOW trong server.env): không ký")
		}
		if out := s.LAN.OutsidePrefixes(allow); len(out) > 0 {
			return fmt.Errorf("sơ đồ LAN có đích nằm ngoài dải được phép (SCS_LAN_ALLOW): %s", strings.Join(out, ", "))
		}
		return nil
	}
}

// Reporter: báo cáo gần nhất của một máy.
type Reporter struct {
	DeviceID   int64          `json:"device_id"`
	Store      string         `json:"store"`
	ListV      string         `json:"list_v"`
	ReceivedAt time.Time      `json:"received_at"`
	State      string         `json:"state"`
	LANIP      string         `json:"lan_ip,omitempty"`
	N          int            `json:"n"`
	Up         uint64         `json:"-"`
	Skip       uint64         `json:"-"`
	DurMs      int            `json:"dur_ms,omitempty"`
	Miss       map[string]int `json:"miss,omitempty"` // khóa thiết bị → số lượt liên tiếp máy này không thấy mà máy khác thấy
	SavedAt    time.Time      `json:"-"`              // lần ghi DB gần nhất (0 = chưa có dòng)
}

// Checked: báo cáo này có kiểm thật không (máy tắt kiểm / không cùng dải thì không tính).
func (r *Reporter) Checked() bool {
	return r.State == protocol.LANStateOK || r.State == protocol.LANStateICMPDenied
}

// TargetState: trạng thái gộp của một thiết bị ở một siêu thị.
type TargetState struct {
	IP        string    `json:"ip"`
	Port      int       `json:"port,omitempty"`
	Type      string    `json:"type"`
	Label     string    `json:"label,omitempty"`
	Pos       int       `json:"pos"`    // vị trí trong sơ đồ gần nhất (để xếp)
	InMap     bool      `json:"in_map"` // còn trong sơ đồ gần nhất
	FirstSeen time.Time `json:"first,omitzero"`
	LastSeen  time.Time `json:"last,omitzero"`
	LastBy    int64     `json:"by,omitempty"` // máy thấy gần nhất
	Status    string    `json:"st"`           // trạng thái đã ghi lịch sử gần nhất
	Since     time.Time `json:"since,omitzero"`
	Day       string    `json:"day,omitempty"` // ngày (UTC) của DayN
	DayN      int       `json:"dn,omitempty"`  // số lần đổi trạng thái đã ghi trong ngày
	Flap      bool      `json:"flap,omitempty"`
}

// Site: trạng thái gộp của một siêu thị.
type Site struct {
	Store   string                  `json:"-"`
	ListV   string                  `json:"list_v"`
	Targets map[string]*TargetState `json:"t"`
	SavedAt time.Time               `json:"-"` // lần ghi DB gần nhất (0 = chưa có dòng)
}

// Change: một lần thiết bị đổi trạng thái (lịch sử 10 ngày).
type Change struct {
	Store string    `json:"store"`
	Key   string    `json:"key"`
	IP    string    `json:"ip"`
	Type  string    `json:"type"`
	Label string    `json:"label,omitempty"`
	From  string    `json:"from"`
	To    string    `json:"to"`
	At    time.Time `json:"at"`
	By    int64     `json:"by,omitempty"`
}

var (
	listVRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
	hexRe   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	states  = []string{protocol.LANStateOK, protocol.LANStateProbesOff, protocol.LANStateICMPDenied,
		protocol.LANStateNoLAN, protocol.LANStateUnsupported}
)

// ParseReport kiểm báo cáo agent gửi và đối chiếu sơ đồ m (đúng ListV, đúng số đích). Lỗi = bỏ báo cáo.
func ParseReport(r *protocol.LANReport, m *rules.LAN) (up, skip uint64, lanIP string, err error) {
	if r == nil || m == nil {
		return 0, 0, "", errors.New("thiếu báo cáo / sơ đồ")
	}
	if !listVRe.MatchString(r.ListV) || r.ListV != m.ListV() {
		return 0, 0, "", errors.New("list_v không khớp sơ đồ")
	}
	if r.N != len(m.Targets) || r.N < 1 || r.N > protocol.MaxLANBits {
		return 0, 0, "", errors.New("số đích khác sơ đồ")
	}
	if !slices.Contains(states, r.State) {
		return 0, 0, "", errors.New("trạng thái lạ")
	}
	parse := func(h string) (uint64, error) {
		if h == "" {
			return 0, nil
		}
		if !hexRe.MatchString(h) {
			return 0, errors.New("bitmap sai dạng")
		}
		return strconv.ParseUint(h, 16, 64)
	}
	if up, err = parse(r.Up); err != nil {
		return
	}
	if skip, err = parse(r.Skip); err != nil {
		return
	}
	mask := uint64(1)<<r.N - 1
	if r.N >= 64 {
		mask = ^uint64(0)
	}
	if up&^mask != 0 || skip&^mask != 0 || up&skip != 0 {
		return 0, 0, "", errors.New("bitmap có bit ngoài sơ đồ hoặc vừa có mặt vừa không kiểm")
	}
	if r.LANIP != "" {
		ip, e := netip.ParseAddr(r.LANIP)
		if e != nil || !ip.Is4() || !ip.IsPrivate() || ip.String() != r.LANIP {
			return 0, 0, "", errors.New("lan_ip không hợp lệ")
		}
		lanIP = r.LANIP
	}
	return up, skip, lanIP, nil
}

// StatusAt: trạng thái của thiết bị theo lần thấy gần nhất (chưa xét "không rõ").
func StatusAt(t *TargetState, now time.Time) string {
	switch {
	case t.LastSeen.IsZero():
		return StNever
	case now.Sub(t.LastSeen) <= Window:
		return StOnline
	case now.Sub(t.LastSeen) <= GoneAfter:
		return StAbsent
	}
	return StGone
}

// Merge đưa báo cáo rep (đã ParseReport) của một máy vào trạng thái siêu thị. m: sơ đồ của báo cáo. Trả các lần
// đổi trạng thái cần ghi lịch sử, và dòng siêu thị / dòng máy có cần ghi DB không.
func Merge(site *Site, rep *Reporter, prev *Reporter, m *rules.LAN, now time.Time) (changes []Change, saveSite, saveRep bool) {
	if site.Targets == nil {
		site.Targets = map[string]*TargetState{}
	}
	if site.ListV != rep.ListV {
		site.ListV, saveSite = rep.ListV, true
	}
	inMap := map[string]bool{}
	for i, t := range m.Targets {
		k := t.Key()
		inMap[k] = true
		ts := site.Targets[k]
		if ts == nil {
			ts = &TargetState{IP: t.IP, Status: StNever}
			if t.KindOf() == "tcp" {
				ts.Port = t.Port
			}
			site.Targets[k] = ts
			saveSite = true
		}
		if ts.Type != t.Type || ts.Label != t.Label || ts.Pos != i || !ts.InMap {
			ts.Type, ts.Label, ts.Pos, ts.InMap = t.Type, t.Label, i, true
			saveSite = true
		}
	}
	for k, ts := range site.Targets {
		if !inMap[k] && ts.InMap {
			ts.InMap, saveSite = false, true
		}
	}

	// Lần thấy + lệch.
	miss := map[string]int{}
	if rep.Checked() {
		for i, t := range m.Targets {
			bit := uint64(1) << i
			if rep.Skip&bit != 0 {
				continue
			}
			k := t.Key()
			ts := site.Targets[k]
			if rep.Up&bit != 0 {
				if ts.FirstSeen.IsZero() {
					ts.FirstSeen, saveSite = now, true
				}
				ts.LastSeen, ts.LastBy = now, rep.DeviceID
				continue
			}
			// Không thấy: máy khác vừa thấy → nghi đường mạng của máy này.
			if !ts.LastSeen.IsZero() && ts.LastBy != rep.DeviceID && now.Sub(ts.LastSeen) <= LechFresh {
				n := 1
				if prev != nil {
					n = prev.Miss[k] + 1
				}
				miss[k] = n
			}
		}
	}
	if len(miss) > 0 {
		rep.Miss = miss
	} else {
		rep.Miss = nil
	}

	// Đổi trạng thái → lịch sử (có giới hạn mỗi ngày).
	day := now.UTC().Format(time.DateOnly)
	keys := make([]string, 0, len(site.Targets))
	for k := range site.Targets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ts := site.Targets[k]
		st := StatusAt(ts, now)
		if st == ts.Status {
			continue
		}
		from := ts.Status
		ts.Status, ts.Since, saveSite = st, now, true
		if from == StNever && st == StOnline && ts.FirstSeen.Equal(now) && site.SavedAt.IsZero() {
			continue // siêu thị mới có dữ liệu lần đầu: không ghi lịch sử cho mọi thiết bị
		}
		if ts.Day != day {
			ts.Day, ts.DayN, ts.Flap = day, 0, false
		}
		if ts.DayN >= MaxChangesPerDay {
			ts.Flap = true
			continue
		}
		ts.DayN++
		changes = append(changes, Change{Store: site.Store, Key: k, IP: ts.IP, Type: ts.Type, Label: ts.Label,
			From: from, To: st, At: now, By: ts.LastBy})
	}
	if now.Sub(site.SavedAt) >= SaveEvery {
		saveSite = true
	}

	saveRep = prev == nil || prev.SavedAt.IsZero() || now.Sub(prev.SavedAt) >= SaveEvery ||
		prev.ListV != rep.ListV || prev.State != rep.State || prev.LANIP != rep.LANIP || prev.Up != rep.Up ||
		prev.Skip != rep.Skip || !sameMiss(prev.Miss, rep.Miss)
	return changes, saveSite, saveRep
}

func sameMiss(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

package lan

import (
	"sort"
	"time"

	"github.com/thangkes/SCS-System/common/rules"
)

// Device: máy đã duyệt của siêu thị (đối chiếu máy thu ngân quét được với máy có agent).
type Device struct {
	ID       int64  `json:"id"`
	Hostname string `json:"hostname"`
	TnNo     int    `json:"tn_no,omitempty"`
	Online   bool   `json:"online"` // còn liên lạc (không mất liên lạc)
}

// Đối chiếu máy thu ngân (type "tn").
const (
	TNMatch   = "khop"       // quét thấy và có máy agent ở đúng IP
	TNNoAgent = "chua_agent" // quét thấy nhưng không máy agent nào ở IP này
	TNNone    = ""           // không quét thấy
)

// TargetView: một thiết bị khi hiển thị.
type TargetView struct {
	Key       string    `json:"key"`
	IP        string    `json:"ip"`
	Port      int       `json:"port,omitempty"`
	Type      string    `json:"type"`
	Label     string    `json:"label,omitempty"`
	Pos       int       `json:"pos"`
	Status    string    `json:"status"`
	FirstSeen time.Time `json:"first_seen,omitzero"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	LastBy    string    `json:"last_by,omitempty"`
	SeenBy    int       `json:"seen_by"`        // số máy (còn báo trong 5 giờ) thấy ở lượt gần nhất
	Checkers  int       `json:"checkers"`       // số máy (còn báo trong 5 giờ) đã kiểm thiết bị này
	Lech      []string  `json:"lech,omitempty"` // máy liên tục không thấy (nghi đường mạng của máy đó)
	Flap      bool      `json:"flap,omitempty"`
	TN        string    `json:"tn,omitempty"`      // đối chiếu máy thu ngân
	TNHost    string    `json:"tn_host,omitempty"` // máy agent ở IP này
}

// ReporterView: một máy đã báo.
type ReporterView struct {
	DeviceID   int64     `json:"device_id"`
	Hostname   string    `json:"hostname"`
	State      string    `json:"state"`
	LANIP      string    `json:"lan_ip,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
	Fresh      bool      `json:"fresh"`
	Seen       int       `json:"seen"`
	Checked    int       `json:"checked"`
	Abnormal   bool      `json:"abnormal,omitempty"`
	WrongIP    bool      `json:"wrong_ip,omitempty"` // máy thu ngân có agent nhưng không ở IP nào của sơ đồ
}

// StoreView: chi tiết một siêu thị.
type StoreView struct {
	Store     string         `json:"store"`
	Unknown   bool           `json:"unknown"` // không máy nào báo trong 5 giờ
	Present   int            `json:"present"`
	EverSeen  int            `json:"ever_seen"`
	Missing   int            `json:"missing"` // vắng + lâu không thấy
	Lech      int            `json:"lech"`
	Targets   []TargetView   `json:"targets"`
	Reporters []ReporterView `json:"reporters"`
	// Đối chiếu máy thu ngân.
	TNScanned int            `json:"tn_scanned"` // máy thu ngân quét thấy (online)
	TNAgents  int            `json:"tn_agents"`  // máy đã duyệt có agent ở siêu thị
	TNNoAgent int            `json:"tn_no_agent"`
	TNWrongIP int            `json:"tn_wrong_ip"`
	NoReport  []string       `json:"no_report,omitempty"` // máy có agent chưa gửi báo cáo LAN (agent cũ / bộ rule chưa có sơ đồ)
	LastAt    time.Time      `json:"last_at,omitzero"`
	MissingBy map[string]int `json:"missing_by,omitempty"` // loại → số thiết bị vắng
}

// View dựng chi tiết siêu thị từ trạng thái gộp, báo cáo các máy, danh sách máy đã duyệt và các sơ đồ.
func View(site *Site, reps []Reporter, devs []Device, maps map[string]*rules.LAN, now time.Time) StoreView {
	v := StoreView{Store: site.Store, MissingBy: map[string]int{}}
	host := map[int64]string{}
	for _, d := range devs {
		host[d.ID] = d.Hostname
	}
	// Máy còn báo (trong 5 giờ, đã kiểm thật) và bitmap của chúng theo khóa thiết bị.
	type fr struct {
		r       *Reporter
		up, chk map[string]bool
	}
	var fresh []fr
	byLANIP := map[string]int64{}
	reported := map[int64]bool{}
	for i := range reps {
		r := &reps[i]
		reported[r.DeviceID] = true
		if r.ReceivedAt.After(v.LastAt) {
			v.LastAt = r.ReceivedAt
		}
		isFresh := now.Sub(r.ReceivedAt) <= Window
		rv := ReporterView{DeviceID: r.DeviceID, Hostname: host[r.DeviceID], State: r.State, LANIP: r.LANIP,
			ReceivedAt: r.ReceivedAt, Fresh: isFresh}
		m := maps[r.ListV]
		f := fr{r: r, up: map[string]bool{}, chk: map[string]bool{}}
		if m != nil && r.Checked() {
			for i, t := range m.Targets {
				bit := uint64(1) << i
				if r.Skip&bit != 0 {
					continue
				}
				f.chk[t.Key()] = true
				rv.Checked++
				if r.Up&bit != 0 {
					f.up[t.Key()] = true
					rv.Seen++
				}
			}
		}
		if isFresh && r.LANIP != "" {
			byLANIP[r.LANIP] = r.DeviceID
		}
		if isFresh && m != nil && r.Checked() {
			fresh = append(fresh, f)
		}
		v.Reporters = append(v.Reporters, rv)
	}
	v.Unknown = len(fresh) == 0
	// Máy báo bất thường: thấy < 1/2 trung vị các máy khác (cần ≥ 2 máy còn báo).
	abnormal := map[int64]bool{}
	if len(fresh) >= 2 {
		for _, f := range fresh {
			var others []int
			for _, g := range fresh {
				if g.r.DeviceID != f.r.DeviceID {
					others = append(others, len(g.up))
				}
			}
			sort.Ints(others)
			med := others[len(others)/2]
			if med >= 2 && len(f.up)*2 < med {
				abnormal[f.r.DeviceID] = true
			}
		}
	}
	for i := range v.Reporters {
		v.Reporters[i].Abnormal = abnormal[v.Reporters[i].DeviceID]
	}

	tnIPs := map[string]bool{}
	for k, ts := range site.Targets {
		if !ts.InMap {
			continue
		}
		tv := TargetView{Key: k, IP: ts.IP, Port: ts.Port, Type: ts.Type, Label: ts.Label, Pos: ts.Pos,
			Status: StatusAt(ts, now), FirstSeen: ts.FirstSeen, LastSeen: ts.LastSeen, LastBy: host[ts.LastBy], Flap: ts.Flap}
		if v.Unknown && tv.Status != StNever {
			tv.Status = StUnknown
		}
		for _, f := range fresh {
			if f.chk[k] {
				tv.Checkers++
			}
			if f.up[k] {
				tv.SeenBy++
			}
			if !abnormal[f.r.DeviceID] && f.r.Miss[k] >= LechRounds && tv.Status == StOnline {
				tv.Lech = append(tv.Lech, host[f.r.DeviceID])
			}
		}
		if len(tv.Lech) > 0 {
			v.Lech++
		}
		if ts.Type == "tn" {
			tnIPs[ts.IP] = true
			if tv.Status == StOnline {
				v.TNScanned++
				if id, ok := byLANIP[ts.IP]; ok {
					tv.TN, tv.TNHost = TNMatch, host[id]
				} else {
					tv.TN = TNNoAgent
					v.TNNoAgent++
				}
			}
		}
		switch tv.Status {
		case StOnline:
			v.Present++
		case StAbsent, StGone:
			v.Missing++
			v.MissingBy[ts.Type]++
		}
		if !ts.FirstSeen.IsZero() {
			v.EverSeen++
		}
		v.Targets = append(v.Targets, tv)
	}
	sort.Slice(v.Targets, func(i, j int) bool { return v.Targets[i].Pos < v.Targets[j].Pos })
	v.TNAgents = len(devs)
	for i := range v.Reporters {
		r := &v.Reporters[i]
		if r.Fresh && r.LANIP != "" && len(tnIPs) > 0 && !tnIPs[r.LANIP] {
			r.WrongIP = true
			v.TNWrongIP++
		}
	}
	for _, d := range devs {
		if !reported[d.ID] {
			v.NoReport = append(v.NoReport, d.Hostname)
		}
	}
	sort.Strings(v.NoReport)
	if len(v.MissingBy) == 0 {
		v.MissingBy = nil
	}
	return v
}

// Summary: một dòng siêu thị trên trang danh sách (bỏ chi tiết thiết bị cho nhẹ; giữ chấm màu theo vị trí).
type Summary struct {
	StoreView
	Dots []Dot `json:"dots"`
}

// Dot: một chấm trên hàng siêu thị.
type Dot struct {
	Type   string `json:"t"`
	Label  string `json:"l,omitempty"`
	IP     string `json:"ip"`
	Status string `json:"s"`
	Lech   bool   `json:"x,omitempty"`
	TN     string `json:"tn,omitempty"`
}

// Summarize: bản gọn của View cho trang danh sách.
func Summarize(v StoreView) Summary {
	s := Summary{StoreView: v}
	for _, t := range v.Targets {
		s.Dots = append(s.Dots, Dot{Type: t.Type, Label: t.Label, IP: t.IP, Status: t.Status, Lech: len(t.Lech) > 0, TN: t.TN})
	}
	s.Targets = nil
	return s
}

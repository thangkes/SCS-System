package app

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// Thống kê agent cho trang "Agent: cập nhật, cấu hình": tiến độ phát hành bản agent, phân bố phiên bản,
// tình trạng bộ rule, và bảng theo trạng thái máy (mới chờ duyệt / hoạt động / mất liên lạc / đã đóng).

// FleetRow: một máy, đủ để thống kê (không gửi ra web).
type FleetRow struct {
	ID       int64
	Status   string // pending | approved | rejected | revoked
	TestRing bool
	Version  string    // bản agent theo heartbeat gần nhất (máy chờ duyệt: bản lúc đăng ký)
	LastSeen time.Time // zero = chưa từng gọi về
	// Rules: agent có báo bộ rule không (agent bản cũ: không), đã cài, đang quét.
	RulesReported, RulesInstalled, Scanning bool
	CertExpires                             time.Time // zero = chưa cấp / không rõ
}

// ProgressPoint: một mốc tiến độ phát hành.
type ProgressPoint struct {
	At       time.Time `json:"at"`
	Version  string    `json:"version"`
	OnTarget int       `json:"on_target"`
	Total    int       `json:"total"`
}

// FleetStore: phần kho cần cho thống kê agent (PostgreSQL; kho giả trong test).
type FleetStore interface {
	FleetRows(ctx context.Context) ([]FleetRow, error)
	RecordRolloutProgress(ctx context.Context, p ProgressPoint, keep time.Duration) error
	RolloutProgress(ctx context.Context, version string, since time.Time) ([]ProgressPoint, error)
}

// FleetGroup: một dòng của bảng theo trạng thái. Con trỏ nil = không áp dụng (hiện "—").
type FleetGroup struct {
	Key        string `json:"key"` // pending | active | offline | closed | approved (tổng máy đã duyệt)
	Total      int    `json:"total"`
	OnTarget   *int   `json:"on_target"`
	NotUpdated *int   `json:"not_updated"`
	Rules      *int   `json:"rules"`
	NoRules    *int   `json:"no_rules"`
}

// FleetStats: dữ liệu trang.
type FleetStats struct {
	Target   Rollout        `json:"target"` // bản đang phát hành (nhóm thử / mọi máy)
	Approved int            `json:"approved"`
	OnTarget int            `json:"on_target"` // máy đã duyệt chạy đúng bản của nhóm mình
	Pending  int            `json:"pending"`
	Groups   []FleetGroup   `json:"groups"`
	Versions []VersionCount `json:"versions"` // máy đã duyệt, nhiều trước
	Rules    struct {
		Scanning     int `json:"scanning"`     // đã cài, đang quét
		Installed    int `json:"installed"`    // đã cài, chưa quét
		None         int `json:"none"`         // agent báo chưa có bộ nào
		NotReported  int `json:"not_reported"` // agent bản cũ không báo
		OfflineAfter int `json:"offline_min"`  // ngưỡng mất liên lạc (phút), để web ghi chú
	} `json:"rules"`
	// Certs: chứng chỉ máy đã duyệt — còn dưới 30 ngày (agent bản mới tự gia hạn), đã hết hạn (máy không
	// gửi được nữa), không rõ hạn; Next: hạn sớm nhất còn hiệu lực.
	Certs struct {
		Soon    int       `json:"soon"`
		Expired int       `json:"expired"`
		Unknown int       `json:"unknown"`
		Next    time.Time `json:"next,omitzero"`
	} `json:"certs"`
	Downloads *DownloadStats  `json:"downloads,omitempty"` // nil: cổng agent chạy tiến trình khác
	Progress  []ProgressPoint `json:"progress"`            // tiến độ bản đang phát hành cho mọi máy
}

// VersionCount: số máy đã duyệt theo bản agent ("" = chưa báo).
type VersionCount struct {
	Version string `json:"version"`
	Devices int    `json:"devices"`
}

func intp(n int) *int { return &n }

// buildFleet gom FleetRow thành FleetStats. offlineAfter: quá bấy lâu không nghe máy là mất liên lạc.
func buildFleet(rows []FleetRow, ro Rollout, now time.Time, offlineAfter time.Duration) FleetStats {
	st := FleetStats{Target: ro, Groups: []FleetGroup{}, Versions: []VersionCount{}, Progress: []ProgressPoint{}}
	st.Rules.OfflineAfter = int(offlineAfter / time.Minute)
	type acc struct{ total, on, not, rules, noRules int }
	g := map[string]*acc{"pending": {}, "active": {}, "offline": {}, "closed": {}, "approved": {}}
	vers := map[string]int{}
	for _, r := range rows {
		target := ro.All
		if r.TestRing && ro.Test != "" {
			target = ro.Test
		}
		var key string
		switch r.Status {
		case DevicePending:
			key = "pending"
		case DeviceApproved:
			key = "active"
			if r.LastSeen.IsZero() || now.Sub(r.LastSeen) > offlineAfter {
				key = "offline"
			}
		default:
			g["closed"].total++
			continue
		}
		for _, k := range []string{key, "approved"} {
			if k == "approved" && key == "pending" {
				continue
			}
			a := g[k]
			a.total++
			if target != "" {
				if r.Version == target {
					a.on++
				} else {
					a.not++
				}
			}
			if r.RulesInstalled {
				a.rules++
			} else {
				a.noRules++
			}
		}
		if key == "pending" {
			continue
		}
		vers[r.Version]++
		switch e := r.CertExpires; {
		case e.IsZero():
			st.Certs.Unknown++
		case !now.Before(e):
			st.Certs.Expired++
		default:
			if e.Sub(now) < 30*24*time.Hour {
				st.Certs.Soon++
			}
			if st.Certs.Next.IsZero() || e.Before(st.Certs.Next) {
				st.Certs.Next = e
			}
		}
		switch {
		case !r.RulesReported:
			st.Rules.NotReported++
		case r.RulesInstalled && r.Scanning:
			st.Rules.Scanning++
		case r.RulesInstalled:
			st.Rules.Installed++
		default:
			st.Rules.None++
		}
	}
	hasTarget := ro.All != "" || ro.Test != ""
	for _, k := range []string{"pending", "active", "offline", "closed", "approved"} {
		a := g[k]
		fg := FleetGroup{Key: k, Total: a.total}
		if k != "closed" && hasTarget {
			fg.OnTarget, fg.NotUpdated = intp(a.on), intp(a.not)
		}
		if k != "closed" && k != "pending" { // máy chờ duyệt chưa gửi heartbeat: không biết rule
			fg.Rules, fg.NoRules = intp(a.rules), intp(a.noRules)
		}
		st.Groups = append(st.Groups, fg)
	}
	st.Approved, st.OnTarget, st.Pending = g["approved"].total, g["approved"].on, g["pending"].total
	for v, n := range vers {
		st.Versions = append(st.Versions, VersionCount{Version: v, Devices: n})
	}
	sort.Slice(st.Versions, func(i, j int) bool {
		if st.Versions[i].Devices != st.Versions[j].Devices {
			return st.Versions[i].Devices > st.Versions[j].Devices
		}
		return st.Versions[i].Version > st.Versions[j].Version
	})
	return st
}

// fleet: GET /admin/v1/agent/fleet — thống kê agent (mức Chỉ xem).
func (a agentAPI) fleet(w http.ResponseWriter, r *http.Request) {
	if !(deviceAPI{a.d}).ready(w) {
		return
	}
	fs, ok := a.d.Registry.(FleetStore)
	if !ok {
		httpx.WriteError(w, http.StatusServiceUnavailable, "kho chưa hỗ trợ thống kê agent")
		return
	}
	ro, err := a.d.Registry.GetRollout(r.Context())
	if err != nil {
		a.d.Log.Error("đọc bản đang phát hành thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	rows, err := fs.FleetRows(r.Context())
	if err != nil {
		a.d.Log.Error("đọc thống kê agent thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	st := buildFleet(rows, ro, time.Now(), a.offlineAfter())
	st.Downloads = a.d.Downloads.Stats()
	if ro.All != "" {
		p, err := fs.RolloutProgress(r.Context(), ro.All, time.Now().Add(-7*24*time.Hour))
		if err != nil {
			a.d.Log.Error("đọc tiến độ phát hành thất bại", "err", err)
		} else if p != nil {
			st.Progress = p
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "fleet": st})
}

func (a agentAPI) offlineAfter() time.Duration {
	if a.d.Cfg != nil && a.d.Cfg.OfflineAfter > 0 {
		return a.d.Cfg.OfflineAfter
	}
	return 60 * time.Minute
}

// rolloutEvery: nhịp ghi tiến độ phát hành.
const rolloutEvery = 5 * time.Minute

// RolloutRecorder ghi tiến độ bản đang phát hành cho mọi máy, 5 phút một dòng, giữ keep.
func RolloutRecorder(d Deps, keep time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		fs, ok := d.Registry.(FleetStore)
		if !ok {
			<-ctx.Done()
			return nil
		}
		for {
			now := time.Now()
			next := now.Truncate(rolloutEvery).Add(rolloutEvery)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(next.Sub(now)):
			}
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := recordRollout(c, d, fs, next, keep); err != nil {
				d.Log.Warn("ghi tiến độ phát hành thất bại", "err", err)
			}
			cancel()
		}
	}
}

func recordRollout(ctx context.Context, d Deps, fs FleetStore, at time.Time, keep time.Duration) error {
	ro, err := d.Registry.GetRollout(ctx)
	if err != nil || ro.All == "" {
		return err
	}
	rows, err := fs.FleetRows(ctx)
	if err != nil {
		return err
	}
	p := ProgressPoint{At: at.UTC(), Version: ro.All}
	for _, r := range rows {
		if r.Status == DeviceApproved {
			p.Total++
			if r.Version == ro.All {
				p.OnTarget++
			}
		}
	}
	return fs.RecordRolloutProgress(ctx, p, keep)
}

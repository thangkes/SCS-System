// Package periods: dòng thời gian của từng máy theo giai đoạn (docs/thiet-ke-luu-theo-giai-doan.md).
//
// Không lưu từng sự kiện, từng nhịp heartbeat: mỗi máy luôn có đúng một giai đoạn đang mở —
//
//	ok       chỉ có sự kiện info / low / medium (hoặc không có gì); kéo dài theo heartbeat
//	alert    có sự kiện high / critical; high tự đóng sau Policy.AutoClose không có sự kiện nặng mới,
//	         đã lên critical thì chỉ admin đóng
//	offline  quá Policy.OfflineAfter không nghe máy gửi gì; tự đóng khi máy gửi lại
//
// Mất liên lạc khi đang có cảnh báo: cảnh báo giữ nguyên (đánh dấu OfflineSince), không bị che.
//
// Gói này chỉ tính toán (không đụng DB): nhận giai đoạn đang mở, trả các thay đổi cần ghi.
package periods

import (
	"errors"
	"slices"
	"sort"
	"time"
	"unicode/utf8"
)

// Loại giai đoạn.
const (
	KindOK      = "ok"
	KindAlert   = "alert"
	KindOffline = "offline"
)

// Người / lý do đóng giai đoạn.
const (
	ClosedAuto    = "auto"   // cảnh báo high hết hạn không có sự kiện nặng mới
	ClosedSystem  = "system" // sự kiện nặng mở cảnh báo, máy gửi lại, mất liên lạc, máy bị thu hồi
	closedByAdmin = "admin:"
)

// Giới hạn bản tóm tắt.
const (
	MaxRules   = 64   // số rule tóm tắt trong một giai đoạn; quá thì gom vào OtherRule
	MaxSample  = 2048 // dòng log mẫu
	MaxNote    = 500
	OtherRule  = "_khac"
	maxCounter = 1_000_000_000
)

// Policy: các hạn (cấu hình được).
type Policy struct {
	AutoClose    time.Duration // cảnh báo high tự đóng sau bấy lâu không có sự kiện high / critical mới
	OfflineAfter time.Duration // không nghe máy gửi gì bấy lâu → mất liên lạc
}

// DefaultPolicy: 24 giờ, 30 phút (3 nhịp heartbeat tối đa 10 phút).
var DefaultPolicy = Policy{AutoClose: 24 * time.Hour, OfflineAfter: 30 * time.Minute}

// severityRank: thứ tự mức nghiêm trọng (giống common/rules.Severities).
var severityRank = map[string]int{"info": 1, "low": 2, "medium": 3, "high": 4, "critical": 5}

// ValidSeverity: mức có hợp lệ không.
func ValidSeverity(s string) bool { return severityRank[s] > 0 }

// Serious: high / critical mở cảnh báo.
func Serious(s string) bool { return severityRank[s] >= severityRank["high"] }

func maxSeverity(a, b string) string {
	if severityRank[b] > severityRank[a] {
		return b
	}
	return a
}

// RuleSum: tóm tắt một rule trong giai đoạn.
type RuleSum struct {
	Severity string    `json:"severity"`
	Count    int       `json:"count"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
	Sample   string    `json:"sample,omitempty"` // dòng log mẫu gần nhất
}

// Period: một giai đoạn. EndedAt zero = đang mở.
type Period struct {
	ID           int64               `json:"id"`
	DeviceID     int64               `json:"device_id"`
	Kind         string              `json:"kind"`
	StartedAt    time.Time           `json:"started_at"`
	EndedAt      time.Time           `json:"ended_at,omitzero"`
	Severity     string              `json:"severity,omitempty"` // mức cao nhất trong giai đoạn ("" = chưa có sự kiện)
	Events       int                 `json:"events"`
	Rules        map[string]*RuleSum `json:"rules"`
	OfflineSince time.Time           `json:"offline_since,omitzero"` // alert: đang mất liên lạc từ
	LastSerious  time.Time           `json:"last_serious,omitzero"`  // alert: sự kiện high / critical gần nhất
	ClosedBy     string              `json:"closed_by,omitempty"`
	Note         string              `json:"note,omitempty"`
}

// Open: giai đoạn còn mở.
func (p *Period) Open() bool { return p != nil && p.EndedAt.IsZero() }

// AdminOnly: cảnh báo đã lên critical, chỉ admin đóng được.
func (p *Period) AdminOnly() bool { return p.Kind == KindAlert && p.Severity == "critical" }

// Event: một (nhóm) sự kiện khớp rule do agent gửi (agent đã gom theo aggregate / cooldown của rule).
type Event struct {
	RuleID   string
	Severity string
	Count    int
	First    time.Time
	Last     time.Time
	Sample   string
}

// Result: các thay đổi cần ghi. Closed = giai đoạn vừa đóng (đã đặt EndedAt, ClosedBy); Opened = giai
// đoạn mới (chưa có ID); Updated = giai đoạn đang mở có thay đổi (bản tóm tắt, mức, OfflineSince).
// OfflineAlert: +1 = mở cảnh báo mất liên lạc, -1 = đóng cảnh báo mất liên lạc, 0 = không đổi.
type Result struct {
	Closed       *Period
	Opened       *Period
	Updated      *Period
	OfflineAlert int
}

// Current: giai đoạn đang mở sau khi áp Result (nil nếu không có).
func (r Result) Current(prev *Period) *Period {
	switch {
	case r.Opened != nil:
		return r.Opened
	case r.Updated != nil:
		return r.Updated
	case r.Closed != nil:
		return nil
	}
	return prev
}

// Empty: không có gì cần ghi.
func (r Result) Empty() bool {
	return r.Closed == nil && r.Opened == nil && r.Updated == nil && r.OfflineAlert == 0
}

func newPeriod(deviceID int64, kind string, at time.Time) *Period {
	return &Period{DeviceID: deviceID, Kind: kind, StartedAt: at, Rules: map[string]*RuleSum{}}
}

func clone(p *Period) *Period {
	c := *p
	c.Rules = make(map[string]*RuleSum, len(p.Rules))
	for k, v := range p.Rules {
		x := *v
		c.Rules[k] = &x
	}
	return &c
}

func closeAt(p *Period, at time.Time, by string) *Period {
	c := clone(p)
	if at.Before(c.StartedAt) {
		at = c.StartedAt
	}
	c.EndedAt, c.ClosedBy = at, by
	return c
}

// Seen: máy vừa gửi tin (heartbeat, sự kiện). Chưa có giai đoạn → mở OK; đang mất liên lạc → đóng,
// mở OK; cảnh báo đang đánh dấu mất liên lạc → bỏ đánh dấu.
func Seen(cur *Period, deviceID int64, now time.Time) Result {
	switch {
	case !cur.Open():
		return Result{Opened: newPeriod(deviceID, KindOK, now)}
	case cur.Kind == KindOffline:
		return Result{Closed: closeAt(cur, now, ClosedSystem), Opened: newPeriod(deviceID, KindOK, now), OfflineAlert: -1}
	case cur.Kind == KindAlert && !cur.OfflineSince.IsZero():
		u := clone(cur)
		u.OfflineSince = time.Time{}
		return Result{Updated: u, OfflineAlert: -1}
	}
	return Result{}
}

// AddEvents: cộng sự kiện vào giai đoạn đang mở (gọi sau Seen). Sự kiện high / critical khi đang OK
// thì đóng OK và mở cảnh báo; khi đang cảnh báo thì cộng vào (critical nâng mức).
func AddEvents(cur *Period, deviceID int64, evs []Event, now time.Time) Result {
	var res Result
	if !cur.Open() {
		res = Seen(cur, deviceID, now)
		cur = res.Current(cur)
	}
	var work *Period // bản đang sửa (giai đoạn mở hiện tại)
	isNew := false
	if res.Opened != nil {
		work, isNew = res.Opened, true
	} else {
		work = clone(cur)
	}
	changed := false
	// Xử lý theo thứ tự thời gian để giai đoạn cảnh báo bắt đầu đúng lúc.
	evs = slices.Clone(evs)
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].Last.Before(evs[j].Last) })
	for _, e := range evs {
		if !ValidSeverity(e.Severity) || e.RuleID == "" {
			continue
		}
		if e.Count < 1 {
			e.Count = 1
		}
		if e.Last.IsZero() || e.Last.After(now) {
			e.Last = now
		}
		if e.First.IsZero() || e.First.After(e.Last) {
			e.First = e.Last
		}
		if Serious(e.Severity) && work.Kind != KindAlert {
			// Đóng giai đoạn hiện tại (OK) và mở cảnh báo, tại giờ sự kiện (không trước giờ bắt đầu).
			at := e.First
			if at.Before(work.StartedAt) {
				at = work.StartedAt
			}
			if work.Events == 0 && !at.After(work.StartedAt) {
				// Giai đoạn OK vừa mở, chưa có gì (độ dài 0): đổi luôn thành cảnh báo, không để lại
				// một giai đoạn OK rỗng.
				work.Kind = KindAlert
			} else {
				res.Closed = closeAt(work, at, ClosedSystem)
				work, isNew = newPeriod(deviceID, KindAlert, at), true
			}
			changed = true
		}
		addEvent(work, e)
		if Serious(e.Severity) && e.Last.After(work.LastSerious) {
			work.LastSerious = e.Last
		}
		changed = true
	}
	if !changed {
		return res
	}
	if isNew {
		res.Opened = work
	} else {
		res.Updated = work
	}
	return res
}

func addEvent(p *Period, e Event) {
	p.Severity = maxSeverity(p.Severity, e.Severity)
	p.Events = min(p.Events+e.Count, maxCounter)
	key := e.RuleID
	if _, ok := p.Rules[key]; !ok && len(p.Rules) >= MaxRules {
		key = OtherRule
	}
	s := p.Rules[key]
	if s == nil {
		s = &RuleSum{Severity: e.Severity, First: e.First, Last: e.Last}
		p.Rules[key] = s
	}
	s.Severity = maxSeverity(s.Severity, e.Severity)
	s.Count = min(s.Count+e.Count, maxCounter)
	if e.First.Before(s.First) {
		s.First = e.First
	}
	if e.Last.After(s.Last) {
		s.Last = e.Last
	}
	if e.Sample != "" && !e.Last.Before(s.Last) {
		s.Sample = truncate(e.Sample, MaxSample)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Tick: kiểm định kỳ một máy. lastSeen = lần cuối nghe máy gửi.
//   - OK mà quá hạn không nghe gì → đóng OK tại lastSeen, mở mất liên lạc (từ lastSeen), báo cảnh báo.
//   - Cảnh báo mà quá hạn không nghe gì → đánh dấu mất liên lạc (cảnh báo giữ nguyên), báo cảnh báo.
//   - Cảnh báo high quá Policy.AutoClose không có sự kiện nặng mới → tự đóng; mở OK (hoặc mất liên lạc
//     nếu máy vẫn không gửi gì).
func Tick(cur *Period, lastSeen, now time.Time, pol Policy) Result {
	if !cur.Open() {
		return Result{}
	}
	stale := !lastSeen.IsZero() && now.Sub(lastSeen) > pol.OfflineAfter
	switch cur.Kind {
	case KindOK:
		if stale {
			return Result{Closed: closeAt(cur, lastSeen, ClosedSystem),
				Opened: newPeriod(cur.DeviceID, KindOffline, maxTime(lastSeen, cur.StartedAt)), OfflineAlert: +1}
		}
	case KindAlert:
		var res Result
		work := cur
		if stale && cur.OfflineSince.IsZero() {
			work = clone(cur)
			work.OfflineSince = lastSeen
			res.Updated, res.OfflineAlert = work, +1
		}
		quiet := work.LastSerious
		if quiet.IsZero() {
			quiet = work.StartedAt
		}
		if !work.AdminOnly() && now.Sub(quiet) >= pol.AutoClose {
			res.Updated = nil
			res.Closed = closeAt(work, now, ClosedAuto)
			kind := KindOK
			if stale {
				kind = KindOffline // cảnh báo mất liên lạc (đã mở) tiếp tục cho tới khi máy gửi lại
			}
			res.Opened = newPeriod(cur.DeviceID, kind, now)
		}
		return res
	}
	return Result{}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// ErrNotAlert: chỉ cảnh báo mới "xử lý" được.
var ErrNotAlert = errors.New("giai đoạn này không phải cảnh báo đang mở")

// Resolve: admin bấm "Đã xử lý" một cảnh báo đang mở. Mở OK (hoặc mất liên lạc nếu máy đang không gửi gì).
func Resolve(cur *Period, by, note string, lastSeen, now time.Time, pol Policy) (Result, error) {
	if !cur.Open() || cur.Kind != KindAlert {
		return Result{}, ErrNotAlert
	}
	c := closeAt(cur, now, closedByAdmin+truncate(by, 80))
	c.Note = truncate(note, MaxNote)
	kind := KindOK
	if !lastSeen.IsZero() && now.Sub(lastSeen) > pol.OfflineAfter {
		kind = KindOffline
	}
	return Result{Closed: c, Opened: newPeriod(cur.DeviceID, kind, now)}, nil
}

// Retire: máy bị thu hồi / không còn được duyệt: đóng giai đoạn đang mở, không mở giai đoạn mới.
func Retire(cur *Period, now time.Time) Result {
	if !cur.Open() {
		return Result{}
	}
	r := Result{Closed: closeAt(cur, now, ClosedSystem)}
	if cur.Kind == KindOffline || (cur.Kind == KindAlert && !cur.OfflineSince.IsZero()) {
		r.OfflineAlert = -1
	}
	return r
}

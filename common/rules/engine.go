package rules

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Line: một dòng log đưa vào Engine. Source là đường dẫn file hoặc "journald"; Unit là unit
// systemd của dòng journal ("kernel" cho log kernel).
type Line struct {
	Source string
	Unit   string
	Time   time.Time
	Text   string
}

// Event: một lần rule báo. Với rule có aggregate, Count là số dòng khớp trong cửa sổ.
type Event struct {
	RuleID   string            `json:"rule_id"`
	Severity string            `json:"severity"`
	Source   string            `json:"source"`
	Unit     string            `json:"unit,omitempty"`
	First    time.Time         `json:"first"`
	Last     time.Time         `json:"last"`
	Count    int               `json:"count"`
	Sample   string            `json:"sample"` // dòng khớp cuối cùng, đã cắt
	Fields   map[string]string `json:"fields,omitempty"`
	Tags     []string          `json:"tags,omitempty"` // tags của rule (schema 2)
}

// MaxKeysPerRule: số khóa nhóm (ví dụ số IP khác nhau) một rule giữ cùng lúc; vượt thì bỏ khóa mới
// để log rác không làm agent ăn hết RAM.
const MaxKeysPerRule = 4096

type compiled struct {
	Rule
	match, exclude *regexp.Regexp
	groupIdx       int // chỉ số nhóm GroupBy trong match, -1 nếu không có
	state          map[string]*keyState
}

type keyState struct {
	hits      []time.Time // các lần khớp còn trong cửa sổ (tối đa Threshold)
	quietTill time.Time   // đang im lặng (cooldown) tới lúc này
	lastSeen  time.Time
}

// Engine chạy một bộ rule. Không an toàn khi gọi Feed từ nhiều goroutine cùng lúc.
type Engine struct {
	set      *RuleSet
	bySource map[string][]*compiled // khóa: đường dẫn file hoặc "journald"
	skipped  []string               // rule bị bỏ vì applies_to không khớp ngữ cảnh máy
	lastGC   time.Time
}

// Compile kiểm bộ rule (Validate) rồi dựng Engine với mọi rule đang bật (không lọc ngữ cảnh).
func Compile(s *RuleSet) (*Engine, error) { return CompileFor(s, nil) }

// CompileFor như Compile nhưng chỉ giữ rule có applies_to khớp ngữ cảnh máy ctx (nil = không lọc).
// Rule bị bỏ vì ngữ cảnh nằm trong Skipped. Không còn rule nào thì trả ErrNoRules.
func CompileFor(s *RuleSet, ctx Context) (*Engine, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	e := &Engine{set: s, bySource: map[string][]*compiled{}}
	for _, r := range s.Rules {
		if r.Disabled {
			continue
		}
		if !r.Applies(ctx) {
			e.skipped = append(e.skipped, r.ID)
			continue
		}
		c := &compiled{Rule: r, match: regexp.MustCompile(r.Match), groupIdx: -1, state: map[string]*keyState{}}
		if r.Exclude != "" {
			c.exclude = regexp.MustCompile(r.Exclude)
		}
		if r.Aggregate != nil && r.Aggregate.GroupBy != "" {
			c.groupIdx = c.match.SubexpIndex(r.Aggregate.GroupBy)
		}
		e.bySource[r.Source] = append(e.bySource[r.Source], c)
	}
	if len(e.bySource) == 0 {
		return nil, ErrNoRules
	}
	return e, nil
}

// Set: bộ rule Engine đang chạy.
func (e *Engine) Set() *RuleSet { return e.set }

// Skipped: mã các rule không chạy trên máy này vì applies_to không khớp ngữ cảnh.
func (e *Engine) Skipped() []string { return slices.Clone(e.skipped) }

// SourceSpec: một nguồn log agent phải đọc.
type SourceSpec struct {
	Source string   // đường dẫn file hoặc "journald"
	Units  []string // với journald: các unit cần đọc; rỗng = toàn bộ journal
}

// Sources: các nguồn agent cần theo dõi cho bộ rule này (đã sắp xếp, không trùng).
func (e *Engine) Sources() []SourceSpec {
	var out []SourceSpec
	for src, rs := range e.bySource {
		sp := SourceSpec{Source: src}
		if src == SourceJournald {
			all := false
			for _, r := range rs {
				if r.Unit == "" {
					all = true
				} else if !slices.Contains(sp.Units, r.Unit) {
					sp.Units = append(sp.Units, r.Unit)
				}
			}
			if all {
				sp.Units = nil
			}
			slices.Sort(sp.Units)
		}
		out = append(out, sp)
	}
	slices.SortFunc(out, func(a, b SourceSpec) int { return strings.Compare(a.Source, b.Source) })
	return out
}

// Covers: có rule đang bật nào đọc nguồn (và unit) này không. Dùng để cảnh báo khi chạy thử sai nguồn.
func (e *Engine) Covers(source, unit string) bool {
	for _, c := range e.bySource[source] {
		if c.Unit == "" || c.Unit == unit {
			return true
		}
	}
	return false
}

// Feed cho một dòng đi qua các rule của nguồn đó; trả các sự kiện cần báo (thường là không có).
func (e *Engine) Feed(l Line) []Event {
	if l.Time.IsZero() {
		l.Time = time.Now()
	}
	text := truncate(l.Text, MaxLineBytes)
	var out []Event
	for _, c := range e.bySource[l.Source] {
		if c.Source == SourceJournald && c.Unit != "" && c.Unit != l.Unit {
			continue
		}
		m := c.match.FindStringSubmatch(text)
		if m == nil || (c.exclude != nil && c.exclude.MatchString(text)) {
			continue
		}
		if ev, ok := c.hit(l, text, m); ok {
			out = append(out, ev)
		}
	}
	if l.Time.Sub(e.lastGC) > time.Minute {
		e.gc(l.Time)
	}
	return out
}

// hit ghi nhận một dòng khớp; trả sự kiện nếu đủ điều kiện báo.
func (c *compiled) hit(l Line, text string, m []string) (Event, bool) {
	key := ""
	if c.groupIdx >= 0 {
		key = m[c.groupIdx]
	}
	st := c.state[key]
	if st == nil {
		if len(c.state) >= MaxKeysPerRule {
			return Event{}, false
		}
		st = &keyState{}
		c.state[key] = st
	}
	st.lastSeen = l.Time
	if l.Time.Before(st.quietTill) {
		return Event{}, false
	}
	threshold, window := 1, time.Duration(0)
	if a := c.Aggregate; a != nil {
		threshold, window = a.Threshold, time.Duration(a.WindowS)*time.Second
	}
	// Bỏ các lần khớp đã ra khỏi cửa sổ, thêm lần này.
	keep := st.hits[:0]
	for _, t := range st.hits {
		if l.Time.Sub(t) < window {
			keep = append(keep, t)
		}
	}
	st.hits = append(keep, l.Time)
	if len(st.hits) < threshold {
		return Event{}, false
	}
	ev := Event{
		RuleID: c.ID, Severity: c.Severity, Source: l.Source, Unit: l.Unit,
		First: st.hits[0], Last: l.Time, Count: len(st.hits),
		Sample: truncate(text, MaxSample), Fields: fields(c.match, m), Tags: slices.Clone(c.Tags),
	}
	st.hits = st.hits[:0]
	if c.CooldownS > 0 {
		st.quietTill = l.Time.Add(time.Duration(c.CooldownS) * time.Second)
	}
	return ev, true
}

// gc xóa khóa nhóm lâu không gặp để bộ nhớ không phình theo thời gian.
func (e *Engine) gc(now time.Time) {
	e.lastGC = now
	for _, rs := range e.bySource {
		for _, c := range rs {
			idle := time.Duration(c.CooldownS) * time.Second
			if c.Aggregate != nil {
				idle = max(idle, time.Duration(c.Aggregate.WindowS)*time.Second)
			}
			for k, st := range c.state {
				if now.Sub(st.lastSeen) > idle && !now.Before(st.quietTill) {
					delete(c.state, k)
				}
			}
		}
	}
}

func fields(re *regexp.Regexp, m []string) map[string]string {
	var f map[string]string
	for i, n := range re.SubexpNames() {
		if n == "" || i >= len(m) || m[i] == "" {
			continue
		}
		if f == nil {
			f = map[string]string{}
		}
		f[n] = truncate(m[i], MaxFieldValue)
	}
	return f
}

// truncate cắt chuỗi tối đa n byte mà không làm gãy ký tự UTF-8.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	// Ký tự UTF-8 dài tối đa 4 byte: bỏ tối đa 3 byte lẻ ở cuối.
	for i := 0; i < 3 && len(s) > 0; i++ {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size > 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

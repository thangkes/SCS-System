// Package rules: khung rule quét log dùng chung cho agent và server.
//
// Cùng một định nghĩa (RuleSet/Rule), cùng một hàm kiểm tra (Validate) và cùng một bộ máy
// khớp (Engine) chạy ở cả hai phía:
//   - server: đọc file YAML do admin viết, kiểm tra, chạy thử trên log mẫu, ký thành Bundle;
//   - agent: kiểm chữ ký Bundle, kiểm tra lại y như server, rồi cho từng dòng log đi qua Engine.
//
// Rule chỉ gồm điều kiện khớp (regex RE2, chạy tuyến tính nên không có ReDoS) và đếm ngưỡng.
// Rule không chạy lệnh, không đọc file ngoài /var/log, nên bộ rule dù bị can thiệp cũng không
// thực thi được gì trên máy.
package rules

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// SourceJournald: đọc từ systemd journal thay vì file.
const SourceJournald = "journald"

// Mức độ của rule, từ nhẹ tới nặng.
var Severities = []string{"info", "low", "medium", "high", "critical"}

// Giới hạn để một bộ rule luôn nhỏ và rẻ khi chạy trên máy yếu.
const (
	MaxRules      = 500
	MaxPattern    = 1024 // độ dài regex
	MaxFields     = 8    // số nhóm có tên trong match
	MaxTests      = 20   // số dòng ví dụ mỗi loại trong tests
	MaxWindowS    = 86400
	MaxThreshold  = 10000
	MaxTitle      = 120
	MaxLineBytes  = 4096 // dòng log dài hơn bị cắt trước khi khớp
	MaxSample     = 1024 // dòng gửi kèm sự kiện
	MaxFieldValue = 256
	MaxTags       = 8
	MaxApplyKeys  = 8
	MaxApplyVals  = 16
	// SchemaMax: schema cao nhất bản này đọc được. Agent báo "rules/<SchemaMax>" trong heartbeat.
	SchemaMax = 2
)

// RuleSet: một bộ rule, gán cho nhóm máy. Version đổi mỗi lần sửa để agent biết phải tải lại.
type RuleSet struct {
	// Schema: phiên bản cấu trúc bộ rule (bỏ trống = 1). Dùng tags / applies_to thì phải là 2;
	// agent cũ (chỉ hiểu schema 1) từ chối cả bộ có trường lạ và giữ bộ đang dùng.
	Schema      int    `json:"schema,omitempty"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Rules       []Rule `json:"rules"`
}

// Rule: một điều kiện khớp trên một nguồn log.
type Rule struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Severity string `json:"severity"`
	// Source: đường dẫn file tuyệt đối trong /var/log, hoặc "journald".
	Source string `json:"source"`
	// Unit: chỉ dùng với journald; so với trường _SYSTEMD_UNIT của dòng journal (tiến trình nào
	// ghi dòng đó, ví dụ ssh.service), hoặc "kernel" cho log kernel (_TRANSPORT=kernel).
	// Bỏ trống = mọi dòng journal.
	Unit string `json:"unit,omitempty"`
	// Match: regex RE2. Nhóm có tên (?P<ten>...) thành trường của sự kiện.
	Match string `json:"match"`
	// Exclude: regex; dòng khớp Match nhưng cũng khớp Exclude thì bỏ qua.
	Exclude   string     `json:"exclude,omitempty"`
	Aggregate *Aggregate `json:"aggregate,omitempty"`
	// CooldownS: sau khi báo, cùng rule + cùng khóa nhóm im lặng bấy nhiêu giây.
	CooldownS int  `json:"cooldown_s,omitempty"`
	Disabled  bool `json:"disabled,omitempty"`
	// Tags: nhãn phân loại rule (lớp dịch vụ, nhóm, cờ báo cáo...), gửi kèm sự kiện. Cần schema 2.
	Tags []string `json:"tags,omitempty"`
	// AppliesTo: rule chỉ chạy trên máy có ngữ cảnh khớp (xem Context). Các khóa AND, các giá trị
	// trong một khóa OR; bỏ trống = mọi máy. Máy không có khóa đó thì rule KHÔNG chạy. Cần schema 2.
	AppliesTo map[string][]string `json:"applies_to,omitempty"`
	// Tests: dòng ví dụ để kiểm rule khi viết (chỉ dùng ở server, bị bỏ khi đóng gói gửi agent).
	Tests *Tests `json:"tests,omitempty"`
}

// Aggregate: chỉ báo khi khớp ít nhất Threshold lần trong WindowS giây.
type Aggregate struct {
	WindowS   int `json:"window_s"`
	Threshold int `json:"threshold"`
	// GroupBy: đếm riêng theo giá trị một nhóm có tên trong Match (ví dụ ip).
	GroupBy string `json:"group_by,omitempty"`
}

// Tests: dòng phải khớp và dòng không được khớp.
type Tests struct {
	Match   []string `json:"match,omitempty"`
	NoMatch []string `json:"no_match,omitempty"`
}

var (
	idRe      = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)*$`)
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
	unitRe    = regexp.MustCompile(`^[A-Za-z0-9@_.:][A-Za-z0-9@_.:-]{0,127}$`)
	fieldRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	tagRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	ctxKeyRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	ctxValRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// ValidationError gom mọi lỗi của một bộ rule để người viết sửa một lần.
type ValidationError []string

func (v ValidationError) Error() string { return strings.Join(v, "\n") }

// Validate kiểm toàn bộ bộ rule; trả ValidationError liệt kê mọi lỗi.
func (s *RuleSet) Validate() error {
	var errs ValidationError
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if !nameRe.MatchString(s.Name) {
		add("name %q: chỉ chữ thường, số, . _ - (tối đa 64 ký tự)", s.Name)
	}
	if !versionRe.MatchString(s.Version) || strings.Contains(s.Version, "..") {
		add("version %q: chỉ chữ, số, . _ + - (ví dụ 2026.11.10-01)", s.Version)
	}
	if s.Schema < 0 || s.Schema > SchemaMax {
		add("schema %d: bản này chỉ đọc được schema 1 tới %d (cần agent / server mới hơn)", s.Schema, SchemaMax)
	}
	if len(s.Rules) == 0 {
		add("rules: bộ rule trống")
	}
	if len(s.Rules) > MaxRules {
		add("rules: %d rule, tối đa %d", len(s.Rules), MaxRules)
	}
	seen := map[string]bool{}
	for i := range s.Rules {
		r := &s.Rules[i]
		where := fmt.Sprintf("rule #%d", i+1)
		if r.ID != "" {
			where = "rule " + r.ID
		}
		for _, e := range r.validate() {
			add("%s: %s", where, e)
		}
		if (len(r.Tags) > 0 || r.AppliesTo != nil) && s.Schema < 2 {
			add("%s: tags / applies_to cần khai \"schema: 2\" ở đầu bộ rule (agent cũ không đọc được)", where)
		}
		if seen[r.ID] {
			add("%s: id bị trùng", where)
		}
		seen[r.ID] = true
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

func (r *Rule) validate() []string {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if !idRe.MatchString(r.ID) || len(r.ID) > 48 {
		add("id %q: chữ in hoa, số, nối bằng - (ví dụ SSH-FAIL-01)", r.ID)
	}
	if utf8.RuneCountInString(r.Title) > MaxTitle || !printable(r.Title) {
		add("title: tối đa %d ký tự, không có ký tự điều khiển", MaxTitle)
	}
	if !slices.Contains(Severities, r.Severity) {
		add("severity %q: phải là một trong %s", r.Severity, strings.Join(Severities, ", "))
	}
	switch {
	case r.Source == SourceJournald:
		if r.Unit != "" && !unitRe.MatchString(r.Unit) {
			add("unit %q: không phải tên unit systemd hợp lệ", r.Unit)
		}
	case r.Source == "":
		add("source: thiếu (đường dẫn file trong /var/log hoặc journald)")
	default:
		if !ValidLogPath(r.Source) {
			add("source %q: phải là đường dẫn file tuyệt đối trong /var/log, không có .. hay ký tự đặc biệt", r.Source)
		}
		if r.Unit != "" {
			add("unit: chỉ dùng khi source là journald")
		}
	}
	match, err := compile("match", r.Match, true)
	if err != nil {
		add("%v", err)
	}
	if r.Exclude != "" {
		if _, err := compile("exclude", r.Exclude, false); err != nil {
			add("%v", err)
		}
	}
	var names []string
	if match != nil {
		for _, n := range match.SubexpNames() {
			if n == "" {
				continue
			}
			if !fieldRe.MatchString(n) {
				add("match: tên nhóm %q phải là chữ thường, số, _ (ví dụ ip, user)", n)
			}
			names = append(names, n)
		}
		if len(names) > MaxFields {
			add("match: %d nhóm có tên, tối đa %d", len(names), MaxFields)
		}
	}
	if a := r.Aggregate; a != nil {
		if a.WindowS < 1 || a.WindowS > MaxWindowS {
			add("aggregate.window_s: từ 1 tới %d giây", MaxWindowS)
		}
		if a.Threshold < 1 || a.Threshold > MaxThreshold {
			add("aggregate.threshold: từ 1 tới %d", MaxThreshold)
		}
		if a.GroupBy != "" && match != nil && !slices.Contains(names, a.GroupBy) {
			add("aggregate.group_by %q: match không có nhóm (?P<%s>...)", a.GroupBy, a.GroupBy)
		}
	}
	if r.CooldownS < 0 || r.CooldownS > MaxWindowS {
		add("cooldown_s: từ 0 tới %d giây", MaxWindowS)
	}
	if len(r.Tags) > MaxTags {
		add("tags: tối đa %d nhãn", MaxTags)
	}
	for i, t := range r.Tags {
		if !tagRe.MatchString(t) {
			add("tags %q: chữ thường, số, - (tối đa 32 ký tự, ví dụ bao-mat, t2)", t)
		}
		if slices.Contains(r.Tags[:i], t) {
			add("tags %q: bị trùng", t)
		}
	}
	if r.AppliesTo != nil && len(r.AppliesTo) == 0 {
		add("applies_to: để trống thì bỏ hẳn trường này (áp cho mọi máy)")
	}
	if len(r.AppliesTo) > MaxApplyKeys {
		add("applies_to: tối đa %d khóa", MaxApplyKeys)
	}
	for k, vals := range r.AppliesTo {
		if !ValidContextKey(k) {
			add("applies_to khóa %q: chữ thường, số, _ (ví dụ role, os, store)", k)
		}
		if len(vals) == 0 || len(vals) > MaxApplyVals {
			add("applies_to.%s: từ 1 tới %d giá trị", k, MaxApplyVals)
		}
		for _, v := range vals {
			if !ValidContextValue(v) {
				add("applies_to.%s %q: chữ thường, số, . _ - (ví dụ pri, ubuntu-22.04, 12345)", k, v)
			}
		}
	}
	if t := r.Tests; t != nil && (len(t.Match) > MaxTests || len(t.NoMatch) > MaxTests) {
		add("tests: tối đa %d dòng mỗi loại", MaxTests)
	}
	return errs
}

func compile(field, expr string, required bool) (*regexp.Regexp, error) {
	if expr == "" {
		if required {
			return nil, fmt.Errorf("%s: thiếu regex", field)
		}
		return nil, nil
	}
	if len(expr) > MaxPattern {
		return nil, fmt.Errorf("%s: regex dài %d ký tự, tối đa %d", field, len(expr), MaxPattern)
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("%s: regex sai: %v", field, err)
	}
	if re.MatchString("") {
		return nil, fmt.Errorf("%s: regex khớp cả dòng trống (sẽ khớp mọi dòng)", field)
	}
	return re, nil
}

// ValidLogPath: file log agent được phép đọc — tuyệt đối, nằm trong /var/log, không có .. hay ký tự lạ.
func ValidLogPath(p string) bool {
	if len(p) > 256 || !strings.HasPrefix(p, "/var/log/") || path.Clean(p) != p {
		return false
	}
	for _, c := range p {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._-@", c)) {
			return false
		}
	}
	return true
}

func printable(s string) bool {
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// ForAgent trả bản sao bỏ tests (agent không cần) và rule đã tắt.
func (s *RuleSet) ForAgent() *RuleSet {
	out := &RuleSet{Schema: s.Schema, Name: s.Name, Version: s.Version, Description: s.Description}
	for _, r := range s.Rules {
		if r.Disabled {
			continue
		}
		r.Tests = nil
		out.Rules = append(out.Rules, r)
	}
	return out
}

// ErrNoRules: bộ rule không còn rule nào đang bật.
var ErrNoRules = errors.New("không có rule nào đang bật")

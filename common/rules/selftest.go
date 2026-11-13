package rules

import "fmt"

// RunTests chạy phần tests của từng rule (dòng phải khớp / không được khớp, đã tính exclude).
// Chỉ kiểm điều kiện khớp, không tính aggregate. Trả danh sách lỗi, rỗng là đạt.
func (s *RuleSet) RunTests() []string {
	var fails []string
	for _, r := range s.Rules {
		if r.Tests == nil {
			continue
		}
		// Giữ schema của bộ gốc: rule dùng tags / applies_to cần schema 2.
		one := &RuleSet{Schema: s.Schema, Name: "test", Version: "test", Rules: []Rule{r}}
		one.Rules[0].Aggregate, one.Rules[0].CooldownS, one.Rules[0].Disabled = nil, 0, false
		e, err := Compile(one)
		if err != nil {
			fails = append(fails, err.Error()) // lỗi Validate đã ghi sẵn "rule <ID>: ..."
			continue
		}
		// Không aggregate, không cooldown: mỗi dòng khớp báo ngay, các dòng không ảnh hưởng nhau.
		feed := func(text string) bool {
			return len(e.Feed(Line{Source: r.Source, Unit: r.Unit, Text: text})) > 0
		}
		for _, l := range r.Tests.Match {
			if !feed(l) {
				fails = append(fails, fmt.Sprintf("rule %s: dòng phải khớp nhưng không khớp: %q", r.ID, l))
			}
		}
		for _, l := range r.Tests.NoMatch {
			if feed(l) {
				fails = append(fails, fmt.Sprintf("rule %s: dòng không được khớp nhưng lại khớp: %q", r.ID, l))
			}
		}
	}
	return fails
}

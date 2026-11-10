package rulefile

import (
	"path/filepath"
	"strings"
	"testing"
)

// Mọi bộ rule mẫu trong server/rules phải luôn hợp lệ và qua hết tests của chính nó.
func TestSampleFiles(t *testing.T) {
	files, _ := filepath.Glob("../../rules/*.yaml")
	if len(files) == 0 {
		t.Fatal("không thấy bộ rule mẫu")
	}
	for _, f := range files {
		s, err := Load(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if fails := s.RunTests(); len(fails) > 0 {
			t.Errorf("%s:\n%s", f, strings.Join(fails, "\n"))
		}
	}
}

func TestParseErrors(t *testing.T) {
	ok := "name: x\nversion: '1'\nrules:\n  - id: A-1\n    severity: low\n    source: journald\n    match: abc\n"
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"gõ sai tên trường": strings.Replace(ok, "severity", "serverity", 1),
		"version là số":     strings.Replace(ok, "'1'", "1.5", 1),
		"YAML sai thụt lề":  strings.Replace(ok, "    match", "  match", 1),
		"thiếu match":       strings.Replace(ok, "    match: abc\n", "", 1),
		"tab trong YAML":    strings.Replace(ok, "    severity", "\tseverity", 1),
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: phải báo lỗi", name)
		}
	}
}

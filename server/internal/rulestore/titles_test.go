package rulestore

import "testing"

func TestTitles(t *testing.T) {
	s, _ := newStore(t)
	if len(s.Titles()) != 0 {
		t.Fatal("kho trống phải không có tiêu đề")
	}
	if _, err := s.Add(yamlVer("1", ""), nil); err != nil {
		t.Fatal(err)
	}
	s.titlesAt = s.titlesAt.Add(-2 * titlesTTL) // hết hạn bộ nhớ đệm
	if got := s.Titles()["SSH-FAIL-01"]; got != "SSH sai" {
		t.Fatalf("tiêu đề: %q", got)
	}
}

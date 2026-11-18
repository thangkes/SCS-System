package app

import (
	"strings"
	"testing"
)

func TestNormalizeStore(t *testing.T) {
	s, msg := normalizeStore(StoreInfo{Code: "12345", Name: "  Siêu   thị A ", Manager: "Nguyễn Văn A",
		IPs: []string{" 203.0.113.10 ", "203.0.113.10", "", "::ffff:198.51.100.7"}, Note: "dòng 1\r\ndòng 2"})
	if msg != "" {
		t.Fatal(msg)
	}
	if s.Name != "Siêu thị A" || s.Note != "dòng 1\ndòng 2" {
		t.Fatalf("chuẩn hóa chữ: %+v", s)
	}
	if strings.Join(s.IPs, ",") != "203.0.113.10,198.51.100.7" {
		t.Fatalf("IP (bỏ trùng, bỏ trống, bỏ dạng IPv4-mapped): %v", s.IPs)
	}
	bad := []StoreInfo{
		{Code: "12a"},
		{Code: "1", Name: strings.Repeat("x", 121)},
		{Code: "1", Manager: "Tên‮đảo"},
		{Code: "1", Leader: "a\x07b"},
		{Code: "1", IPs: []string{"không phải ip"}},
		{Code: "1", IPs: []string{"0.0.0.0"}},
		{Code: "1", IPs: []string{"1.1.1.1", "1.1.1.2", "1.1.1.3", "1.1.1.4", "1.1.1.5", "1.1.1.6", "1.1.1.7", "1.1.1.8", "1.1.1.9", "1.1.1.10"}},
	}
	for i, in := range bad {
		if _, msg := normalizeStore(in); msg == "" {
			t.Errorf("trường hợp %d phải bị từ chối: %+v", i, in)
		}
	}
	if s, _ := normalizeStore(StoreInfo{Code: "7"}); s.IPs == nil {
		t.Fatal("IPs rỗng phải là mảng rỗng (JSON [])")
	}
}

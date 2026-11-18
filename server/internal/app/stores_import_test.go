package app

import (
	"strings"
	"testing"
)

func TestPlanImport(t *testing.T) {
	existing := []StoreInfo{{Code: "100", Name: "Cũ", Manager: "An", IPs: []string{"203.0.113.1"}}}
	rows := [][]string{
		{}, // dòng trống trước tiêu đề
		{"Store Code", "Store Name", "Ip", "Manager", "Cột lạ", "Ghi chú"},
		{"100", "", "", "Bình", "x", ""}, // đổi quản lý, giữ tên và IP
		{"101", "Mới", "203.0.113.5; 203.0.113.6", "", "", "chú"},
		{"100", "lặp", "", "", "", ""},           // trùng mã
		{"abc", "", "", "", "", ""},              // mã sai
		{"102", "", "không-phải-ip", "", "", ""}, // IP sai
		{"103", "", "", "", "", ""},              // mới, chỉ có mã
		{"", "", "", "", "", ""},                 // dòng trống bị bỏ
	}
	p, err := planImport(rows, existing)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 6 || p.New != 2 || p.Changed != 1 || p.Errors != 3 || p.Same != 0 {
		t.Fatalf("đếm: %+v", p)
	}
	if strings.Join(p.Unknown, ",") != "Cột lạ" {
		t.Fatalf("cột lạ: %v", p.Unknown)
	}
	if p.Rows[0].Status != "error" {
		t.Fatal("dòng lỗi phải hiện trước")
	}
	var saved100, saved101 StoreInfo
	for _, s := range p.save {
		switch s.Code {
		case "100":
			saved100 = s
		case "101":
			saved101 = s
		}
	}
	if saved100.Name != "Cũ" || saved100.Manager != "Bình" || strings.Join(saved100.IPs, ",") != "203.0.113.1" {
		t.Fatalf("ô trống phải giữ nguyên: %+v", saved100)
	}
	if strings.Join(saved101.IPs, ",") != "203.0.113.5,203.0.113.6" || saved101.Note != "chú" {
		t.Fatalf("dòng mới: %+v", saved101)
	}
	if _, err := planImport([][]string{{"Tên"}, {"a"}}, nil); err == nil {
		t.Fatal("thiếu cột mã phải lỗi")
	}
	// Tiêu đề tiếng Việt không dấu và có dấu.
	p, err = planImport([][]string{{"ma sieu thi", "Tên siêu thị", "Phó phòng"}, {"7", "Bảy", "Dũng"}}, nil)
	if err != nil || p.New != 1 || p.save[0].DeputyDepartment != "Dũng" {
		t.Fatalf("tiêu đề tiếng Việt: %v %+v", err, p)
	}
}

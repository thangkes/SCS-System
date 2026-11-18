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
	p, err := planImport(rows, existing, ImportOptions{})
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
	if _, err := planImport([][]string{{"Tên"}, {"a"}}, nil, ImportOptions{}); err == nil {
		t.Fatal("thiếu cột mã phải lỗi")
	}
	// Tiêu đề tiếng Việt không dấu và có dấu.
	p, err = planImport([][]string{{"ma sieu thi", "Tên siêu thị", "Phó phòng"}, {"7", "Bảy", "Dũng"}}, nil, ImportOptions{})
	if err != nil || p.New != 1 || p.save[0].DeputyDepartment != "Dũng" {
		t.Fatalf("tiêu đề tiếng Việt: %v %+v", err, p)
	}
}

func TestImportMappingOverwrite(t *testing.T) {
	existing := []StoreInfo{{Code: "100", Name: "Cũ", Manager: "An", Note: "giữ", IPs: []string{"203.0.113.1"}}}
	rows := [][]string{{"Cửa hàng", "Tên gọi", "Người QL", "IP"}, {"100", "", "Bình", ""}}

	// Tên cột lạ: tự nhận không ra cột mã → báo lỗi nhưng vẫn trả danh sách cột để chọn.
	p, err := planImport(rows, existing, ImportOptions{})
	if err != errNeedCode || len(p.Headers) != 4 {
		t.Fatalf("thiếu cột mã: %v %+v", err, p)
	}
	m, err := parseMapping("0=code,1=name,2=manager,3=ips")
	if err != nil {
		t.Fatal(err)
	}
	// Bổ sung: ô trống giữ nguyên.
	p, err = planImport(rows, existing, ImportOptions{Map: m})
	if err != nil || p.Changed != 1 || p.save[0].Name != "Cũ" || p.save[0].Manager != "Bình" || len(p.save[0].IPs) != 1 {
		t.Fatalf("bổ sung: %v %+v", err, p.save)
	}
	// Ghi đè: ô trống của cột được gán thì xóa; trường không gán cột (ghi chú) vẫn giữ.
	p, err = planImport(rows, existing, ImportOptions{Map: m, Overwrite: true})
	s := p.save[0]
	if err != nil || s.Name != "" || len(s.IPs) != 0 || s.Manager != "Bình" || s.Note != "giữ" {
		t.Fatalf("ghi đè: %v %+v", err, s)
	}
	for _, bad := range []string{"0=code,1=code", "x=code", "0=lạ", "99=code"} {
		if _, err := parseMapping(bad); err == nil {
			t.Errorf("mapping %q phải lỗi", bad)
		}
	}
	if m, err := parseMapping("0=code,1="); err != nil || len(m) != 1 {
		t.Fatalf("cột bỏ qua: %v %v", m, err)
	}
}

package tabular

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func makeXLSX(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

const sheetXML = `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="D1" t="inlineStr"><is><t>IP</t></is></c></row>
<row r="2"><c r="A2"><v>36372</v></c><c r="B2" t="s"><v>2</v></c><c r="D2" t="str"><v>203.0.113.1</v></c></row>
<row r="4"><c r="A4"><v>1.2E3</v></c></row>
</sheetData></worksheet>`

const sstXML = `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<si><t>Mã siêu thị</t></si><si><t>Tên siêu thị</t></si><si><r><t>Siêu thị </t></r><r><t>Mẫu</t></r></si></sst>`

func TestXLSX(t *testing.T) {
	data := makeXLSX(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="A" sheetId="1" r:id="rId7"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId7" Target="worksheets/data.xml"/></Relationships>`,
		"xl/worksheets/data.xml":     sheetXML,
		"xl/sharedStrings.xml":       sstXML,
	})
	rows, err := Read("cua-hang.XLSX", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("số dòng (giữ dòng trống ở giữa): %d %q", len(rows), rows)
	}
	if strings.Join(rows[0], "|") != "Mã siêu thị|Tên siêu thị||IP" {
		t.Fatalf("dòng tiêu đề: %q", rows[0])
	}
	if strings.Join(rows[1], "|") != "36372|Siêu thị Mẫu||203.0.113.1" {
		t.Fatalf("dòng dữ liệu (chữ định dạng, số, công thức): %q", rows[1])
	}
	if rows[2] != nil || rows[3][0] != "1200" {
		t.Fatalf("dòng trống / số mũ: %q", rows[2:])
	}
}

func TestXLSXLimits(t *testing.T) {
	if _, err := Read("a.xlsx", []byte("không phải zip")); err == nil {
		t.Fatal("file hỏng phải lỗi")
	}
	big := makeXLSX(t, map[string]string{"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row r="999999"><c r="A999999"><v>1</v></c></row></sheetData></worksheet>`})
	if _, err := Read("a.xlsx", big); !errors.Is(err, ErrTooBig) {
		t.Fatalf("quá nhiều dòng: %v", err)
	}
	wide := makeXLSX(t, map[string]string{"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row r="1"><c r="ZZ1"><v>1</v></c></row></sheetData></worksheet>`})
	if _, err := Read("a.xlsx", wide); !errors.Is(err, ErrTooBig) {
		t.Fatalf("quá nhiều cột: %v", err)
	}
	if _, err := Read("a.xls", nil); err == nil || !strings.Contains(err.Error(), ".xlsx") {
		t.Fatalf(".xls: %v", err)
	}
	if _, err := Read("a.pdf", nil); err == nil {
		t.Fatal("đuôi lạ phải lỗi")
	}
	if _, err := Read("a.csv", make([]byte, MaxFile+1)); !errors.Is(err, ErrTooBig) {
		t.Fatal("file quá lớn")
	}
}

func TestCSV(t *testing.T) {
	rows, err := Read("a.csv", []byte("\xef\xbb\xbfMã;Tên\n36372;\"Siêu thị; Mẫu\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0] != "Mã" || rows[1][1] != "Siêu thị; Mẫu" {
		t.Fatalf("BOM, dấu ; và ô có ngoặc kép: %q", rows)
	}
	rows, _ = Read("a.csv", []byte("Mã,Tên\n1,A\n"))
	if rows[1][1] != "A" {
		t.Fatalf("dấu phẩy: %q", rows)
	}
	if _, err := Read("a.csv", []byte("M\xe3,T\n")); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("không phải UTF-8: %v", err)
	}
}

func TestWriteXLSXRoundTrip(t *testing.T) {
	data, err := WriteXLSX("Báo cáo: 11/2026", []string{"Thời điểm", "CPU (%)", "Ghi chú"},
		[][]any{{"2026-11-18 10:00", 12.5, "a < b & \"c\"\x01"}, {"2026-11-18 11:00", int64(3), nil}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ReadXLSX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0][1] != "CPU (%)" || rows[1][1] != "12.5" || rows[1][2] != `a < b & "c"` || rows[2][1] != "3" {
		t.Fatalf("đọc lại: %q", rows)
	}
	if colName(0) != "A" || colName(25) != "Z" || colName(26) != "AA" || sheetName("a/b:c") != "a-b-c" {
		t.Fatal("tên cột / tên trang tính")
	}
}

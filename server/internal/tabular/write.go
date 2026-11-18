package tabular

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// WriteXLSX tạo file Excel một trang tính: dòng tiêu đề (chữ đậm, cố định khi cuộn) và các dòng dữ liệu. Ô kiểu
// float64 / int / int64 ghi thành SỐ (Excel tính toán được, không phụ thuộc dấu thập phân của máy); còn lại là chữ.
func WriteXLSX(sheet string, header []string, rows [][]any) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(body))
		return err
	}
	const xmlHead = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"
	files := []struct{ name, body string }{
		{"[Content_Types].xml", xmlHead + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
			`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
			`</Types>`},
		{"_rels/.rels", xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`</Relationships>`},
		{"xl/workbook.xml", xmlHead + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
			`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` +
			`<sheet name="` + xmlEsc(sheetName(sheet)) + `" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
			`</Relationships>`},
		// Kiểu 0: thường; 1: chữ đậm (tiêu đề).
		{"xl/styles.xml", xmlHead + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
			`<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>` +
			`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
			`<borders count="1"><border/></borders><cellStyleXfs count="1"><xf/></cellStyleXfs>` +
			`<cellXfs count="2"><xf fontId="0" xfId="0"/><xf fontId="1" xfId="0" applyFont="1"/></cellXfs>` +
			`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`},
	}
	for _, f := range files {
		if err := add(f.name, f.body); err != nil {
			return nil, err
		}
	}
	var sb strings.Builder
	sb.WriteString(xmlHead + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	sb.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`)
	sb.WriteString(`<cols>`)
	for i, h := range header {
		w := max(10, min(40, len([]rune(h))+2))
		fmt.Fprintf(&sb, `<col min="%d" max="%d" width="%d" customWidth="1"/>`, i+1, i+1, w)
	}
	sb.WriteString(`</cols><sheetData>`)
	writeRow := func(r int, cells []any, style int) {
		fmt.Fprintf(&sb, `<row r="%d">`, r)
		for c, v := range cells {
			ref := colName(c) + strconv.Itoa(r)
			s := ""
			if style > 0 {
				s = ` s="` + strconv.Itoa(style) + `"`
			}
			switch x := v.(type) {
			case nil:
			case float64:
				if !math.IsNaN(x) && !math.IsInf(x, 0) {
					fmt.Fprintf(&sb, `<c r="%s"%s><v>%s</v></c>`, ref, s, strconv.FormatFloat(x, 'f', -1, 64))
				}
			case int:
				fmt.Fprintf(&sb, `<c r="%s"%s><v>%d</v></c>`, ref, s, x)
			case int64:
				fmt.Fprintf(&sb, `<c r="%s"%s><v>%d</v></c>`, ref, s, x)
			default:
				fmt.Fprintf(&sb, `<c r="%s" t="inlineStr"%s><is><t xml:space="preserve">%s</t></is></c>`, ref, s, xmlEsc(fmt.Sprint(x)))
			}
		}
		sb.WriteString(`</row>`)
	}
	hdr := make([]any, len(header))
	for i, h := range header {
		hdr[i] = h
	}
	writeRow(1, hdr, 1)
	for i, r := range rows {
		writeRow(i+2, r, 0)
	}
	sb.WriteString(`</sheetData></worksheet>`)
	if err := add("xl/worksheets/sheet1.xml", sb.String()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// colName: 0 → A, 26 → AA.
func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

func xmlEsc(s string) string {
	// Bỏ ký tự XML 1.0 không cho phép (ký tự điều khiển trừ tab, xuống dòng).
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r != 0xFFFE && r != 0xFFFF) {
			return r
		}
		return -1
	}, s)
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// sheetName: tên trang tính Excel tối đa 31 ký tự, không chứa : \ / ? * [ ].
func sheetName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`:\/?*[]`, r) {
			return '-'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 31 {
		s = string(r[:31])
	}
	if s == "" {
		s = "Sheet1"
	}
	return s
}

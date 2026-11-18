// Package tabular đọc bảng từ file CSV hoặc XLSX (Excel) chỉ bằng thư viện chuẩn của Go: trả về các dòng ô chữ
// của trang tính đầu tiên. Dùng để nạp thông tin siêu thị hàng loạt.
//
// An toàn: file do người dùng tải lên nên giới hạn kích thước file, kích thước từng phần trong file nén (chống
// "zip bomb"), số dòng, số cột, độ dài ô; XML đọc bằng encoding/xml (không xử lý thực thể ngoài).
package tabular

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Giới hạn.
const (
	MaxFile     = 5 << 20  // file tải lên
	MaxPart     = 32 << 20 // một phần XML sau khi giải nén
	MaxRows     = 20000
	MaxCols     = 40
	MaxCell     = 4000 // ký tự mỗi ô
	maxZipFiles = 2000
)

// ErrTooBig: file / bảng vượt giới hạn.
var ErrTooBig = errors.New("file quá lớn hoặc quá nhiều dòng / cột")

// Read đọc theo đuôi file (.csv, .xlsx).
func Read(name string, data []byte) ([][]string, error) {
	if len(data) > MaxFile {
		return nil, ErrTooBig
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".csv", ".txt":
		return ReadCSV(data)
	case ".xlsx":
		return ReadXLSX(data)
	case ".xls":
		return nil, errors.New("file .xls (Excel 97-2003) chưa hỗ trợ: mở bằng Excel rồi lưu lại dạng .xlsx hoặc .csv")
	}
	return nil, errors.New("chỉ nhận file .xlsx hoặc .csv")
}

// ReadCSV: UTF-8 (có hoặc không BOM); dấu phân cách tự nhận ',' ';' hoặc tab theo dòng đầu.
func ReadCSV(data []byte) ([][]string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return nil, errors.New("file CSV không phải UTF-8: trong Excel chọn Lưu thành \"CSV UTF-8 (Comma delimited)\"")
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	sep := ','
	if c, s, t := bytes.Count(first, []byte(",")), bytes.Count(first, []byte(";")), bytes.Count(first, []byte("\t")); s > c && s >= t {
		sep = ';'
	} else if t > c && t > s {
		sep = '\t'
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes = sep, -1, true
	var out [][]string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("CSV lỗi: %w", err)
		}
		if len(out) >= MaxRows+1 || len(rec) > MaxCols {
			return nil, ErrTooBig
		}
		for i := range rec {
			if utf8.RuneCountInString(rec[i]) > MaxCell {
				return nil, ErrTooBig
			}
		}
		out = append(out, rec)
	}
	return out, nil
}

// ---------------------------------------------------------------- XLSX

func readPart(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > MaxPart {
		return nil, ErrTooBig
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, MaxPart+1)) // không tin kích thước khai trong file
	if err != nil {
		return nil, err
	}
	if len(b) > MaxPart {
		return nil, ErrTooBig
	}
	return b, nil
}

type xRels struct {
	Rel []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

type xWorkbook struct {
	Sheets []struct {
		RID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	} `xml:"sheets>sheet"`
}

// xText: chữ trong <si> / <is>: một <t> hoặc nhiều <r><t> (chữ định dạng).
type xText struct {
	T string   `xml:"t"`
	R []string `xml:"r>t"`
}

func (x xText) String() string {
	if len(x.R) > 0 {
		return strings.Join(x.R, "")
	}
	return x.T
}

// ReadXLSX đọc trang tính đầu tiên của file Excel.
func ReadXLSX(data []byte) ([][]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("không đọc được file .xlsx (file hỏng hoặc không phải Excel)")
	}
	if len(zr.File) > maxZipFiles {
		return nil, ErrTooBig
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	sheet := "xl/worksheets/sheet1.xml"
	if wb, rels := files["xl/workbook.xml"], files["xl/_rels/workbook.xml.rels"]; wb != nil && rels != nil {
		var w xWorkbook
		var r xRels
		if b, err := readPart(wb); err == nil && xml.Unmarshal(b, &w) == nil && len(w.Sheets) > 0 {
			if b, err := readPart(rels); err == nil && xml.Unmarshal(b, &r) == nil {
				for _, rel := range r.Rel {
					if rel.ID == w.Sheets[0].RID {
						t := strings.TrimPrefix(rel.Target, "/")
						if !strings.HasPrefix(t, "xl/") {
							t = path.Join("xl", t)
						}
						sheet = path.Clean(t)
					}
				}
			}
		}
	}
	var shared []string
	if f := files["xl/sharedStrings.xml"]; f != nil {
		b, err := readPart(f)
		if err != nil {
			return nil, err
		}
		var sst struct {
			SI []xText `xml:"si"`
		}
		if err := xml.Unmarshal(b, &sst); err != nil {
			return nil, errors.New("file .xlsx lỗi (bảng chữ)")
		}
		for _, si := range sst.SI {
			shared = append(shared, si.String())
		}
	}
	f := files[sheet]
	if f == nil {
		return nil, errors.New("file .xlsx không có trang tính")
	}
	b, err := readPart(f)
	if err != nil {
		return nil, err
	}
	var ws struct {
		Rows []struct {
			R     int `xml:"r,attr"`
			Cells []struct {
				Ref string `xml:"r,attr"`
				T   string `xml:"t,attr"`
				V   string `xml:"v"`
				IS  xText  `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(b, &ws); err != nil {
		return nil, errors.New("file .xlsx lỗi (trang tính)")
	}
	var out [][]string
	for i, row := range ws.Rows {
		ri := row.R - 1
		if row.R == 0 {
			ri = i
		}
		if ri >= MaxRows+1 || ri < 0 {
			return nil, ErrTooBig
		}
		for len(out) <= ri { // giữ đúng số dòng (dòng trống ở giữa vẫn tính)
			out = append(out, nil)
		}
		var rec []string
		for j, c := range row.Cells {
			col := j
			if c.Ref != "" {
				if col = colIndex(c.Ref); col < 0 {
					continue
				}
			}
			if col >= MaxCols {
				return nil, ErrTooBig
			}
			var v string
			switch c.T {
			case "s":
				n, err := strconv.Atoi(strings.TrimSpace(c.V))
				if err != nil || n < 0 || n >= len(shared) {
					continue
				}
				v = shared[n]
			case "inlineStr":
				v = c.IS.String()
			case "b":
				v = map[string]string{"1": "TRUE", "0": "FALSE"}[c.V]
			default: // số, công thức đã tính (str)
				v = numberText(c.V)
			}
			if utf8.RuneCountInString(v) > MaxCell {
				return nil, ErrTooBig
			}
			for len(rec) <= col {
				rec = append(rec, "")
			}
			rec[col] = v
		}
		out[ri] = rec
	}
	return out, nil
}

// colIndex: "C12" → 2.
func colIndex(ref string) int {
	n := 0
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if c >= 'A' && c <= 'Z' {
			n = n*26 + int(c-'A'+1)
			if n > MaxCols {
				return MaxCols
			}
			continue
		}
		break
	}
	return n - 1
}

// numberText: số Excel "36372" giữ nguyên; "36372.0" / "3.6372E4" đổi về số nguyên khi đúng là số nguyên.
func numberText(v string) string {
	v = strings.TrimSpace(v)
	if f, err := strconv.ParseFloat(v, 64); err == nil && f == float64(int64(f)) && strings.ContainsAny(v, ".eE") {
		return strconv.FormatInt(int64(f), 10)
	}
	return v
}

package app

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/thangkes/SCS-System/server/internal/auth"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/tabular"
)

// Nạp thông tin siêu thị hàng loạt từ file .xlsx / .csv:
//
//	POST /admin/v1/stores/import?file=ten.xlsx          → xem trước (không ghi gì)
//	POST /admin/v1/stores/import?file=ten.xlsx&apply=1  → ghi (một transaction, một dòng audit)
//	GET  /admin/v1/stores/template.csv                  → file mẫu
//
// Dòng đầu (không trống) là tiêu đề cột; nhận tên cột tiếng Việt có dấu / không dấu hoặc tiếng Anh. Bắt buộc có cột
// mã siêu thị. Ô trống = GIỮ giá trị đang có (không xóa); cột IP có giá trị thì THAY cả danh sách IP.

// importColumns: khóa trường → các tên cột chấp nhận (so sau khi chuẩn hóa bằng headerKey).
var importColumns = map[string][]string{
	"code":              {"mã siêu thị", "ma sieu thi", "mã cửa hàng", "ma cua hang", "mã st", "ma st", "mã", "ma", "store code", "store id", "code"},
	"name":              {"tên siêu thị", "ten sieu thi", "tên cửa hàng", "ten cua hang", "tên", "ten", "store name", "name"},
	"organization":      {"tổ chức", "to chuc", "organization", "org"},
	"ips":               {"ip", "ip wan", "ips", "địa chỉ ip", "dia chi ip"},
	"manager":           {"quản lý", "quan ly", "manager"},
	"deputy":            {"phó quản lý", "pho quan ly", "deputy"},
	"leader":            {"trưởng nhóm", "truong nhom", "leader"},
	"deputy_department": {"phó phòng", "pho phong", "deputy department"},
	"note":              {"ghi chú", "ghi chu", "note", "notes"},
}

// importOrder: thứ tự cột của file mẫu và tên hiển thị.
var importOrder = []struct{ key, label string }{
	{"code", "Mã siêu thị"}, {"name", "Tên siêu thị"}, {"organization", "Tổ chức"}, {"ips", "IP"},
	{"manager", "Quản lý"}, {"deputy", "Phó quản lý"}, {"leader", "Trưởng nhóm"},
	{"deputy_department", "Phó phòng"}, {"note", "Ghi chú"},
}

var headerJunk = regexp.MustCompile(`[\s_*:()\-.]+`)

func headerKey(s string) string {
	return headerJunk.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), " ")
}

var headerIndex = func() map[string]string {
	m := map[string]string{}
	for k, names := range importColumns {
		for _, n := range names {
			m[strings.TrimSpace(headerKey(n))] = k
		}
	}
	return m
}()

var ipSplit = regexp.MustCompile(`[\s,;|]+`)

// ImportRow: kết quả xem trước của một dòng.
type ImportRow struct {
	Row     int      `json:"row"` // số dòng trong file (bắt đầu từ 1)
	Code    string   `json:"code"`
	Name    string   `json:"name,omitempty"`
	Status  string   `json:"status"` // new | changed | same | error
	Changes []string `json:"changes,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// ImportPlan: kết quả xem trước cả file.
type ImportPlan struct {
	Columns []string    `json:"columns"` // cột nhận ra (tên hiển thị)
	Unknown []string    `json:"unknown"` // cột không nhận ra (bỏ qua)
	Total   int         `json:"total"`
	New     int         `json:"new"`
	Changed int         `json:"changed"`
	Same    int         `json:"same"`
	Errors  int         `json:"errors"`
	Rows    []ImportRow `json:"rows"` // tối đa 500 dòng, lỗi và thay đổi trước
	save    []StoreInfo
}

var fieldLabel = func() map[string]string {
	m := map[string]string{}
	for _, c := range importOrder {
		m[c.key] = c.label
	}
	return m
}()

// planImport đọc bảng và so với thông tin đang có.
func planImport(rows [][]string, existing []StoreInfo) (*ImportPlan, error) {
	h := 0
	for h < len(rows) && strings.TrimSpace(strings.Join(rows[h], "")) == "" {
		h++
	}
	if h == len(rows) {
		return nil, errors.New("file không có dữ liệu")
	}
	plan := &ImportPlan{Columns: []string{}, Unknown: []string{}, Rows: []ImportRow{}}
	col := map[string]int{}
	for i, name := range rows[h] {
		k, ok := headerIndex[strings.TrimSpace(headerKey(name))]
		switch {
		case strings.TrimSpace(name) == "":
		case !ok:
			plan.Unknown = append(plan.Unknown, strings.TrimSpace(name))
		case col[k] == 0:
			col[k] = i + 1 // lưu i+1 để 0 = không có
			plan.Columns = append(plan.Columns, fieldLabel[k])
		}
	}
	if col["code"] == 0 {
		return nil, errors.New(`không thấy cột mã siêu thị (đặt tên cột là "Mã siêu thị" hoặc "Store Code")`)
	}
	cell := func(r []string, k string) (string, bool) {
		i := col[k] - 1
		if i < 0 || i >= len(r) {
			return "", false
		}
		v := strings.TrimSpace(r[i])
		return v, v != ""
	}
	cur := map[string]StoreInfo{}
	for _, s := range existing {
		cur[s.Code] = s
	}
	seen := map[string]int{}
	for i := h + 1; i < len(rows); i++ {
		r := rows[i]
		if strings.TrimSpace(strings.Join(r, "")) == "" {
			continue
		}
		code, _ := cell(r, "code")
		row := ImportRow{Row: i + 1, Code: code}
		plan.Total++
		fail := func(msg string) {
			row.Status, row.Error = "error", msg
			plan.Errors++
			plan.Rows = append(plan.Rows, row)
		}
		if !storeCodeRe.MatchString(code) {
			fail("mã siêu thị phải là số, tối đa 10 chữ số")
			continue
		}
		if prev, dup := seen[code]; dup {
			fail("trùng mã với dòng " + itoa(prev))
			continue
		}
		seen[code] = i + 1
		old, had := cur[code]
		next := old
		next.Code = code
		if next.IPs == nil {
			next.IPs = []string{}
		}
		set := map[string]*string{"name": &next.Name, "organization": &next.Organization, "manager": &next.Manager,
			"deputy": &next.Deputy, "leader": &next.Leader, "deputy_department": &next.DeputyDepartment, "note": &next.Note}
		for k, p := range set {
			if v, ok := cell(r, k); ok {
				*p = v
			}
		}
		if v, ok := cell(r, "ips"); ok {
			next.IPs = slices.DeleteFunc(ipSplit.Split(v, -1), func(s string) bool { return s == "" })
		}
		norm, msg := normalizeStore(next)
		if msg != "" {
			fail(msg)
			continue
		}
		row.Name = norm.Name
		for _, c := range importOrder[1:] {
			if c.key == "ips" {
				if strings.Join(norm.IPs, ",") != strings.Join(old.IPs, ",") {
					row.Changes = append(row.Changes, c.label)
				}
			} else if *storeField(&norm, c.key) != *storeField(&old, c.key) {
				row.Changes = append(row.Changes, c.label)
			}
		}
		switch {
		case !had:
			row.Status = "new"
			plan.New++
		case len(row.Changes) > 0:
			row.Status = "changed"
			plan.Changed++
		default:
			row.Status = "same"
			plan.Same++
		}
		if row.Status != "same" {
			plan.save = append(plan.save, norm)
		}
		plan.Rows = append(plan.Rows, row)
	}
	// Hiện lỗi trước, rồi mới, đổi, không đổi; tối đa 500 dòng.
	rank := map[string]int{"error": 0, "new": 1, "changed": 2, "same": 3}
	slices.SortStableFunc(plan.Rows, func(a, b ImportRow) int { return rank[a.Status] - rank[b.Status] })
	if len(plan.Rows) > 500 {
		plan.Rows = plan.Rows[:500]
	}
	return plan, nil
}

func storeField(s *StoreInfo, k string) *string {
	switch k {
	case "name":
		return &s.Name
	case "organization":
		return &s.Organization
	case "manager":
		return &s.Manager
	case "deputy":
		return &s.Deputy
	case "leader":
		return &s.Leader
	case "deputy_department":
		return &s.DeputyDepartment
	default:
		return &s.Note
	}
}

// importFile: POST /admin/v1/stores/import (mức 6).
func (a storesAPI) importFile(w http.ResponseWriter, r *http.Request) {
	sd, ok := a.dir(w)
	if !ok {
		return
	}
	name := path.Base(r.URL.Query().Get("file"))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tabular.MaxFile))
	if err != nil {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "file quá lớn (tối đa 5 MB)")
		return
	}
	rows, err := tabular.Read(name, data)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	existing, err := sd.ListStores(r.Context())
	if err != nil {
		a.d.Log.Error("đọc thông tin siêu thị thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	plan, err := planImport(rows, existing)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.URL.Query().Get("apply") != "1" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": plan, "applied": false})
		return
	}
	if plan.Errors > 0 {
		httpx.WriteError(w, http.StatusBadRequest, "file còn "+itoa(plan.Errors)+" dòng lỗi: sửa rồi nạp lại")
		return
	}
	me, _ := auth.FromContext(r.Context())
	if err := sd.SaveStores(r.Context(), plan.save, me.Name, name); err != nil {
		a.d.Log.Error("nạp thông tin siêu thị thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	a.d.Log.Info("nạp thông tin siêu thị từ file", "file", name, "so_sieu_thi", len(plan.save), "by", me.Name)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": plan, "applied": true})
}

// template: GET /admin/v1/stores/template.csv — file mẫu (UTF-8 có BOM để Excel mở đúng tiếng Việt).
func (a storesAPI) template(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf")
	cw := csv.NewWriter(&buf)
	head := make([]string, len(importOrder))
	for i, c := range importOrder {
		head[i] = c.label
	}
	cw.Write(head)
	cw.Write([]string{"12345", "Siêu thị Mẫu", "Công ty Mẫu", "203.0.113.10, 203.0.113.11", "Nguyễn Văn A",
		"Trần Thị B", "Lê Văn C", "Phạm Văn D", "Ô trống = giữ nguyên; cột IP có giá trị thì thay cả danh sách"})
	cw.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mau-thong-tin-sieu-thi.csv"`)
	w.Write(buf.Bytes())
}

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
	"strconv"
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
	// Headers: từng cột trong file và trường đang gán (rỗng = bỏ qua), để người dùng sửa mapping.
	Headers   []ImportHeader `json:"headers"`
	Overwrite bool           `json:"overwrite"`
	save      []StoreInfo
}

// ImportHeader: một cột của file.
type ImportHeader struct {
	Index int    `json:"index"` // vị trí cột, từ 0
	Name  string `json:"name"`  // tên cột trong file
	Field string `json:"field"` // khóa trường (code, name, ...); rỗng = bỏ qua
}

// ImportOptions: Map = vị trí cột → khóa trường do người dùng chọn (nil = tự nhận theo tên cột); Overwrite = ô
// trống XÓA giá trị đang có của trường được gán (mặc định: ô trống giữ nguyên).
type ImportOptions struct {
	Map       map[int]string
	Overwrite bool
}

// parseMapping đọc "0=code,1=name,3=ips" (cột không có trong danh sách = bỏ qua).
func parseMapping(s string) (map[int]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	m := map[int]string{}
	used := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		i, err := strconv.Atoi(k)
		if !ok || err != nil || i < 0 || i >= tabular.MaxCols {
			return nil, errors.New("mapping cột không hợp lệ")
		}
		if v == "" {
			continue
		}
		if _, known := importColumns[v]; !known {
			return nil, errors.New("trường không hợp lệ: " + v)
		}
		if used[v] {
			return nil, errors.New("trường " + fieldLabel[v] + " được gán cho hai cột")
		}
		used[v] = true
		m[i] = v
	}
	return m, nil
}

var fieldLabel = func() map[string]string {
	m := map[string]string{}
	for _, c := range importOrder {
		m[c.key] = c.label
	}
	return m
}()

// planImport đọc bảng và so với thông tin đang có.
func planImport(rows [][]string, existing []StoreInfo, opt ImportOptions) (*ImportPlan, error) {
	h := 0
	for h < len(rows) && strings.TrimSpace(strings.Join(rows[h], "")) == "" {
		h++
	}
	if h == len(rows) {
		return nil, errors.New("file không có dữ liệu")
	}
	plan := &ImportPlan{Columns: []string{}, Unknown: []string{}, Rows: []ImportRow{}, Headers: []ImportHeader{},
		Overwrite: opt.Overwrite}
	col := map[string]int{} // khóa trường → vị trí cột + 1 (0 = không có)
	for i, name := range rows[h] {
		name = strings.TrimSpace(name)
		hd := ImportHeader{Index: i, Name: name}
		if opt.Map != nil {
			hd.Field = opt.Map[i]
		} else if k, ok := headerIndex[strings.TrimSpace(headerKey(name))]; ok && col[k] == 0 {
			hd.Field = k
		}
		if hd.Field != "" {
			col[hd.Field] = i + 1
			plan.Columns = append(plan.Columns, fieldLabel[hd.Field])
		} else if name != "" {
			plan.Unknown = append(plan.Unknown, name)
		}
		if name != "" || hd.Field != "" {
			plan.Headers = append(plan.Headers, hd)
		}
	}
	if col["code"] == 0 {
		// Vẫn trả danh sách cột để người dùng chọn cột mã siêu thị.
		return plan, errNeedCode
	}
	cell := func(r []string, k string) (string, bool) {
		i := col[k] - 1
		if i < 0 || i >= len(r) {
			return "", false
		}
		v := strings.TrimSpace(r[i])
		return v, v != ""
	}
	mapped := func(k string) bool { return col[k] > 0 }
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
			if v, ok := cell(r, k); ok || (opt.Overwrite && mapped(k)) {
				*p = v // ghi đè: ô trống của cột được gán thì xóa giá trị cũ
			}
		}
		if v, ok := cell(r, "ips"); ok || (opt.Overwrite && mapped("ips")) {
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

var errNeedCode = errors.New(`chưa có cột mã siêu thị: chọn cột nào là "Mã siêu thị" trong phần ghép cột`)

// importFile: POST /admin/v1/stores/import?file=&map=0=code,1=name&mode=overwrite[&apply=1] (mức 2, như nạp rule).
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
	mapping, err := parseMapping(r.URL.Query().Get("map"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	opt := ImportOptions{Map: mapping, Overwrite: r.URL.Query().Get("mode") == "overwrite"}
	plan, err := planImport(rows, existing, opt)
	if errors.Is(err, errNeedCode) {
		// Trả cả danh sách cột để web cho chọn cột mã.
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "plan": plan, "applied": false})
		return
	}
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
	a.d.Log.Info("nạp thông tin siêu thị từ file", "file", name, "so_sieu_thi", len(plan.save), "ghi_de", opt.Overwrite, "by", me.Name)
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

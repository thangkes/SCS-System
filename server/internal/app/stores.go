package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/server/internal/auth"
	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// StoreInfo: thông tin một siêu thị (nhập trên web). IPs[0] là IP WAN chính (cột wan_ip), phần còn lại là IP
// WAN thêm; máy gửi từ bất kỳ IP nào trong danh sách đều không bị cảnh báo "IP lạ".
type StoreInfo struct {
	Code             string    `json:"code"`
	Name             string    `json:"name,omitempty"`
	Organization     string    `json:"organization,omitempty"`
	IPs              []string  `json:"ips"`
	Manager          string    `json:"manager,omitempty"`
	Deputy           string    `json:"deputy,omitempty"`
	Leader           string    `json:"leader,omitempty"`
	DeputyDepartment string    `json:"deputy_department,omitempty"`
	Note             string    `json:"note,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitzero"`
	UpdatedBy        string    `json:"updated_by,omitempty"`
}

// StoreDirectory: kho thông tin siêu thị (PostgreSQL). Tách khỏi Registry để kho giả trong test không phải cài.
type StoreDirectory interface {
	ListStores(ctx context.Context) ([]StoreInfo, error)
	SaveStore(ctx context.Context, s StoreInfo, by string) error
}

const maxStoreIPs = 9

// cleanField: bỏ khoảng trắng thừa, từ chối ký tự điều khiển / vô hình (giống tên đăng nhập), giới hạn độ dài.
func cleanField(s string, max int, multiline bool) (string, bool) {
	if multiline {
		s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	} else {
		s = strings.Join(strings.Fields(s), " ")
	}
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if r == '\n' && multiline {
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Co, unicode.Cs) {
			return "", false
		}
	}
	return s, true
}

// normalizeStore kiểm và chuẩn hóa dữ liệu form; trả câu lỗi cho người dùng.
func normalizeStore(in StoreInfo) (StoreInfo, string) {
	out := StoreInfo{Code: in.Code}
	if !storeCodeRe.MatchString(in.Code) {
		return out, "mã siêu thị là số, tối đa 10 chữ số"
	}
	fields := []struct {
		dst      *string
		src, lbl string
		max      int
	}{
		{&out.Name, in.Name, "tên siêu thị", 120}, {&out.Organization, in.Organization, "tổ chức", 80},
		{&out.Manager, in.Manager, "quản lý", 120}, {&out.Deputy, in.Deputy, "phó quản lý", 120},
		{&out.Leader, in.Leader, "trưởng nhóm", 120}, {&out.DeputyDepartment, in.DeputyDepartment, "phó phòng", 120},
	}
	for _, f := range fields {
		v, ok := cleanField(f.src, f.max, false)
		if !ok {
			return out, f.lbl + ": tối đa " + itoa(f.max) + " ký tự, không có ký tự điều khiển"
		}
		*f.dst = v
	}
	note, ok := cleanField(in.Note, 2000, true)
	if !ok {
		return out, "ghi chú: tối đa 2000 ký tự, không có ký tự điều khiển"
	}
	out.Note = note
	seen := map[netip.Addr]bool{}
	for _, s := range in.IPs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		ip, err := netip.ParseAddr(s)
		if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
			return out, "IP không hợp lệ: " + truncText(s, 60)
		}
		ip = ip.Unmap()
		if seen[ip] {
			continue
		}
		seen[ip] = true
		out.IPs = append(out.IPs, ip.String())
	}
	if len(out.IPs) > maxStoreIPs {
		return out, "tối đa " + itoa(maxStoreIPs) + " IP mỗi siêu thị"
	}
	if out.IPs == nil {
		out.IPs = []string{}
	}
	return out, ""
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func truncText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

type storesAPI struct{ d Deps }

func (a storesAPI) dir(w http.ResponseWriter) (StoreDirectory, bool) {
	sd, ok := a.d.Registry.(StoreDirectory)
	if !ok {
		httpx.WriteError(w, http.StatusServiceUnavailable, "chưa có kho thông tin siêu thị")
	}
	return sd, ok
}

// list: GET /admin/v1/stores (mức Chỉ xem).
func (a storesAPI) list(w http.ResponseWriter, r *http.Request) {
	sd, ok := a.dir(w)
	if !ok {
		return
	}
	list, err := sd.ListStores(r.Context())
	if err != nil {
		a.d.Log.Error("đọc thông tin siêu thị thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	if list == nil {
		list = []StoreInfo{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "stores": list})
}

// save: POST /admin/v1/stores/{code} (mức 6 "Chỉnh thông tin agent").
func (a storesAPI) save(w http.ResponseWriter, r *http.Request) {
	sd, ok := a.dir(w)
	if !ok {
		return
	}
	var in StoreInfo
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	in.Code = r.PathValue("code")
	s, msg := normalizeStore(in)
	if msg != "" {
		httpx.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	me, _ := auth.FromContext(r.Context())
	if err := sd.SaveStore(r.Context(), s, me.Name); err != nil {
		a.d.Log.Error("lưu thông tin siêu thị thất bại", "err", err, "store", s.Code)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	a.d.Log.Info("sửa thông tin siêu thị", "store", s.Code, "by", me.Name)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "store": s})
}

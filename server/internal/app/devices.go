package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/server/internal/auth"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/periods"
)

// deviceAPI: API cho trang Chờ duyệt / Máy trên web quản trị.
type deviceAPI struct{ d Deps }

var storeCodeRe = regexp.MustCompile(`^[0-9]{1,10}$`)

// list: GET /admin/v1/devices?status=pending|approved|rejected|revoked (mặc định pending).
func (a deviceAPI) list(w http.ResponseWriter, r *http.Request) {
	if !a.ready(w) {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "":
		status = DevicePending
	case DevicePending, DeviceApproved, DeviceRejected, DeviceRevoked:
	default:
		httpx.WriteError(w, http.StatusBadRequest, "status không hợp lệ")
		return
	}
	list, err := a.d.Registry.ListDevices(r.Context(), status)
	if err != nil {
		a.d.Log.Error("đọc danh sách máy thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	if list == nil {
		list = []Device{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "devices": list})
}

// approve: POST /admin/v1/devices/{id}/approve, body tùy chọn {"store_code": "12345"}
// (bắt buộc khi hostname sai quy ước, vì không đọc được mã siêu thị từ hostname).
func (a deviceAPI) approve(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	var body struct {
		StoreCode string `json:"store_code"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
			return
		}
	}
	if body.StoreCode != "" && !storeCodeRe.MatchString(body.StoreCode) {
		httpx.WriteError(w, http.StatusBadRequest, ErrInvalidStore.Error())
		return
	}
	by := actor(r)
	dev, err := a.d.Registry.Approve(r.Context(), id, by, body.StoreCode)
	if a.fail(w, err, "duyệt máy") {
		return
	}
	a.d.Log.Info("đã duyệt máy", "by", by, "device", id, "hostname", dev.Hostname, "sieu_thi", dev.StoreCode)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "device": dev})
}

// autoApprove: GET /admin/v1/auto-approve — công tắc duyệt tự động và các điều kiện.
func (a deviceAPI) autoApprove(w http.ResponseWriter, r *http.Request) {
	if !a.ready(w) {
		return
	}
	st, err := a.d.Registry.AutoApproveSetting(r.Context())
	if err != nil {
		a.d.Log.Error("đọc công tắc duyệt tự động thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "setting": st, "rules": AutoApproveRules})
}

// setAutoApprove: POST /admin/v1/auto-approve {"on": true|false} (mức Agent, như duyệt tay).
func (a deviceAPI) setAutoApprove(w http.ResponseWriter, r *http.Request) {
	if !a.ready(w) {
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&body); err != nil || body.On == nil {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	by := actor(r)
	if err := a.d.Registry.SetAutoApprove(r.Context(), *body.On, by); err != nil {
		a.d.Log.Error("đổi công tắc duyệt tự động thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	a.d.Log.Warn("đổi công tắc duyệt tự động", "by", by, "bat", *body.On)
	a.autoApprove(w, r)
}

// reject: POST /admin/v1/devices/{id}/reject (máy đang chờ duyệt).
func (a deviceAPI) reject(w http.ResponseWriter, r *http.Request) {
	a.decide(w, r, "từ chối máy", a.d.Registry.Reject)
}

// revoke: POST /admin/v1/devices/{id}/revoke (máy đã duyệt: chứng chỉ hết được nhận).
func (a deviceAPI) revoke(w http.ResponseWriter, r *http.Request) {
	a.decide(w, r, "thu hồi máy", a.d.Registry.Revoke)
}

func (a deviceAPI) decide(w http.ResponseWriter, r *http.Request, what string,
	fn func(ctx context.Context, id int64, by string) error) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	by := actor(r)
	if a.fail(w, fn(r.Context(), id, by), what) {
		return
	}
	a.d.Log.Warn("đã "+what, "by", by, "device", id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a deviceAPI) ready(w http.ResponseWriter) bool {
	if a.d.Registry == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "server chưa kết nối được DB")
		return false
	}
	return true
}

func (a deviceAPI) id(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if !a.ready(w) {
		return 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "mã máy không hợp lệ")
		return 0, false
	}
	return id, true
}

// fail đổi lỗi của kho thành mã HTTP; trả true nếu đã trả lời lỗi.
func (a deviceAPI) fail(w http.ResponseWriter, err error, what string) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrWrongState), errors.Is(err, ErrConflict):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNeedStore), errors.Is(err, ErrInvalidStore):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		a.d.Log.Error(what+" thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
	}
	return true
}

// actor: tên người đang đăng nhập (ghi vào DB: ai duyệt, ai thu hồi).
func actor(r *http.Request) string {
	if s, ok := auth.FromContext(r.Context()); ok && s.Name != "" {
		return s.Name
	}
	return "admin"
}

// alerts: GET /admin/v1/alerts?state=open|all (mặc định open: cảnh báo chưa xác nhận).
func (a deviceAPI) alerts(w http.ResponseWriter, r *http.Request) {
	if !a.ready(w) {
		return
	}
	state := r.URL.Query().Get("state")
	if state != "" && state != "open" && state != "all" {
		httpx.WriteError(w, http.StatusBadRequest, "state không hợp lệ")
		return
	}
	list, err := a.d.Registry.ListAlerts(r.Context(), state == "all")
	if err != nil {
		a.d.Log.Error("đọc danh sách cảnh báo thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	if list == nil {
		list = []Alert{}
	}
	if a.d.Rules != nil {
		titles := a.d.Rules.Titles()
		for i := range list {
			for j := range list[i].Reasons {
				list[i].Reasons[j].Title = titles[list[i].Reasons[j].Rule]
			}
		}
	}
	for i := range list { // sự kiện sức khỏe máy: không nằm trong kho rule
		for j := range list[i].Reasons {
			if t := HealthTitle(list[i].Reasons[j].Rule); t != "" {
				list[i].Reasons[j].Title = t
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "alerts": list, "policy": a.policy()})
}

// policy: hạn tự đóng cảnh báo high và hạn mất liên lạc (phút) để web ghi đúng cấu hình.
func (a deviceAPI) policy() map[string]int {
	p := map[string]int{"autoclose_min": 24 * 60, "offline_min": 15}
	if c := a.d.Cfg; c != nil {
		if c.AlertAutoClose > 0 {
			p["autoclose_min"] = int(c.AlertAutoClose.Minutes())
		}
		if c.OfflineAfter > 0 {
			p["offline_min"] = int(c.OfflineAfter.Minutes())
		}
	}
	return p
}

// ackAlert: POST /admin/v1/alerts/{id}/ack (admin xác nhận đã xem, cảnh báo đóng lại).
func (a deviceAPI) ackAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	by := actor(r)
	if a.fail(w, a.d.Registry.AckAlert(r.Context(), id, by), "xác nhận cảnh báo") {
		return
	}
	a.d.Log.Info("đã xác nhận cảnh báo", "by", by, "alert", id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// timeline: GET /admin/v1/devices/{id}/periods?limit=100 — dòng thời gian theo giai đoạn của một máy
// (ok / cảnh báo / mất liên lạc) và lịch sử trạng thái thay đổi đáng kể.
func (a deviceAPI) timeline(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tl, err := a.d.Registry.ListPeriods(r.Context(), id, limit)
	if a.fail(w, err, "đọc dòng thời gian") {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "timeline": tl, "policy": a.policy()})
}

// resolve: POST /admin/v1/periods/{id}/resolve {note, resolution} — đóng một cảnh báo đang mở (cả mức critical).
// resolution: "fixed" (Đã xử lý, mặc định) | "confirmed" (Xác nhận sự cố — đóng nhanh sự cố một lần).
func (a deviceAPI) resolve(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	var req struct {
		Note       string `json:"note"`
		Resolution string `json:"resolution"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // ghi chú tùy chọn
	note, ok := cleanNote(w, req.Note, false)
	if !ok {
		return
	}
	switch req.Resolution {
	case "":
		req.Resolution = periods.ResolutionFixed
	case periods.ResolutionFixed, periods.ResolutionConfirmed:
	default:
		httpx.WriteError(w, http.StatusBadRequest, "cách đóng không hợp lệ (fixed | confirmed)")
		return
	}
	by := actor(r)
	if a.fail(w, a.d.Registry.ResolvePeriod(r.Context(), id, by, note, req.Resolution), "xử lý cảnh báo") {
		return
	}
	a.d.Log.Warn("admin đóng sự cố", "giai_doan", id, "boi", by, "cach", req.Resolution)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handling: POST /admin/v1/periods/{id}/handling {status, note} — Chờ thông tin / Đã gửi report cho sự cố
// đang mở. Sự cố VẪN MỞ và tự đóng theo mức như cũ (high tự đóng khi yên, critical cần người đóng). Bắt buộc ghi chú.
func (a deviceAPI) handling(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "nội dung không đọc được")
		return
	}
	if req.Status != periods.HandlingWaitingInfo && req.Status != periods.HandlingReported {
		httpx.WriteError(w, http.StatusBadRequest, "trạng thái không hợp lệ (waiting_info | reported)")
		return
	}
	note, ok := cleanNote(w, req.Note, true)
	if !ok {
		return
	}
	by := actor(r)
	if a.fail(w, a.d.Registry.SetHandling(r.Context(), id, req.Status, by, note), "đặt trạng thái xử lý") {
		return
	}
	a.d.Log.Info("admin đặt trạng thái xử lý sự cố", "giai_doan", id, "boi", by, "trang_thai", req.Status)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// cleanNote: ghi chú ≤ 500 ký tự, UTF-8 hợp lệ, bỏ ký tự điều khiển; required = bắt buộc có nội dung.
func cleanNote(w http.ResponseWriter, s string, required bool) (string, bool) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > 500 {
		httpx.WriteError(w, http.StatusBadRequest, "ghi chú tối đa 500 ký tự")
		return "", false
	}
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	if required && strings.TrimSpace(s) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "cần ghi chú (chờ ai / chờ gì, gửi report cho ai)")
		return "", false
	}
	return strings.TrimSpace(s), true
}

package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/thangkes/SCS-System/server/internal/auth"
	"github.com/thangkes/SCS-System/server/internal/httpx"
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "alerts": list})
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

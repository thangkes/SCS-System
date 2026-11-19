package auth

import (
	"encoding/json"
	"net/http"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// rootSubject: tên dùng để đếm lần nhập sai mã SSH của quản trị gốc (tên hiển thị là chữ tự do).
const rootSubject = "quản trị gốc (mã SSH)"

// Quản lý tài khoản được ủy quyền cho mức 1 (Duyệt cụm server), trong phạm vi hẹp:
//   - chỉ thấy tài khoản chờ duyệt và tài khoản mức 5–7;
//   - duyệt / đổi mức chỉ được chọn mức 5–7 (cao nhất là Xác nhận sự cố);
//   - khóa / mở khóa chỉ tài khoản mức 5–7; xóa chỉ tài khoản đang chờ (từ chối đăng ký);
//   - không đụng tới tài khoản mức 1–4, không tự sửa mình, không đổi giới hạn phiên.
//
// Mọi kiểm tra ở server; giao diện chỉ ẩn bớt nút.
const (
	LevelAccountManager = LevelCluster   // mức thấp nhất được vào trang Tài khoản
	DelegateMinLevel    = LevelIncidents // mức 1 chỉ cấp / quản lý được mức này trở xuống (5–7)
)

// delegateSees: mức 1 có thấy / quản lý tài khoản a không.
func delegateSees(a Account) bool {
	return a.Status == AccountPending || a.Level >= DelegateMinLevel
}

// delegateAllowed kiểm một thao tác của người mức 1 (me) trên tài khoản a. Trả "" nếu được, ngược lại là lý do.
func delegateAllowed(me Session, a Account, action string, level Level) string {
	if me.Level == LevelRoot {
		return ""
	}
	if me.Level != LevelAccountManager {
		return "cần quyền: " + LevelNames[LevelAccountManager]
	}
	if me.Account != "" && me.Account == a.Username {
		return "không tự sửa tài khoản của mình"
	}
	if !delegateSees(a) {
		return "mức 1 chỉ quản lý tài khoản mức 5–7 và tài khoản chờ duyệt"
	}
	switch action {
	case "approve", "level":
		if level < DelegateMinLevel {
			return "mức 1 chỉ cấp được mức 5 – Xác nhận sự cố trở xuống (5, 6, 7)"
		}
		if action == "level" && a.Status == AccountPending {
			return "tài khoản đang chờ: hãy duyệt"
		}
	case "disable", "enable":
		if a.Status == AccountPending {
			return "tài khoản đang chờ: hãy duyệt hoặc từ chối"
		}
	case "delete":
		if a.Status != AccountPending {
			return "mức 1 chỉ từ chối được tài khoản chờ duyệt; xóa tài khoản đang dùng cần quản trị gốc"
		}
	case "session":
		return "giới hạn phiên chỉ quản trị gốc đặt"
	}
	return ""
}

// listBlocks: GET /admin/v1/login-blocks (mức 1 trở lên).
func (m *Manager) listBlocks(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "blocks": m.ListBlocks()})
}

// removeBlock: POST /admin/v1/login-blocks/remove {kind, key} (mức 1 trở lên).
func (m *Manager) removeBlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil ||
		(req.Kind != BlockIP && req.Kind != BlockDevice) || req.Key == "" || len(req.Key) > 80 {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	me, _ := FromContext(r.Context())
	n, err := m.Unblock(r.Context(), req.Kind, req.Key, me.Name)
	if err != nil {
		m.log.Error("gỡ khóa đăng nhập thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	if n == 0 {
		httpx.WriteError(w, http.StatusNotFound, "không có khóa này (có thể đã hết hạn)")
		return
	}
	m.log.Warn("gỡ khóa đăng nhập", "loai", req.Kind, "khoa", req.Key, "by", me.Name, "muc", int(me.Level))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

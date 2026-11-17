package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/thangkes/SCS-System/server/internal/addrbook"
	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// serversAPI: trang "Địa chỉ server" — danh sách địa chỉ server gửi cho agent (IP LAN, IP WAN,
// tên miền DDNS, IP VPN) và số máy đã nhận / đang dùng từng địa chỉ.
//
// Quy trình đổi địa điểm / đổi mạng: thêm địa chỉ mới → chờ gần đủ máy nhận danh sách mới (mỗi
// heartbeat 5–10 phút) → chuyển mạng → địa chỉ cũ không còn máy dùng thì gỡ.
type serversAPI struct{ d Deps }

// list: GET /admin/v1/agent/servers.
func (a serversAPI) list(w http.ResponseWriter, r *http.Request) {
	if a.d.Servers == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "tiến trình này không quản lý danh sách địa chỉ")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "stats": a.d.Servers.Snapshot()})
}

type serversReq struct {
	Action string `json:"action"` // add | remove | up | down
	URL    string `json:"url"`
	Note   string `json:"note"`
	First  bool   `json:"first"` // add: đặt lên đầu danh sách
	Force  bool   `json:"force"` // remove: gỡ dù còn máy đang gọi qua địa chỉ này
}

// change: POST /admin/v1/agent/servers {action, url, ...}.
func (a serversAPI) change(w http.ResponseWriter, r *http.Request) {
	b := a.d.Servers
	if b == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "tiến trình này không quản lý danh sách địa chỉ")
		return
	}
	var req serversReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		httpx.WriteError(w, http.StatusBadRequest, "thiếu địa chỉ")
		return
	}
	var err error
	switch req.Action {
	case "add":
		_, err = b.Add(req.URL, req.Note, req.First)
	case "remove":
		if n := b.UsingCount(req.URL); n > 0 && !req.Force {
			httpx.WriteJSON(w, http.StatusConflict, map[string]any{"ok": false, "using": n,
				"error": fmt.Sprintf("còn %d máy đang gọi server qua địa chỉ này trong %d phút qua; "+
					"gỡ bây giờ thì các máy đó chỉ còn các địa chỉ khác trong danh sách", n, int(addrbook.Window.Minutes()))})
			return
		}
		err = b.Remove(req.URL)
	case "up":
		err = b.Move(req.URL, -1)
	case "down":
		err = b.Move(req.URL, 1)
	default:
		httpx.WriteError(w, http.StatusBadRequest, "thao tác không hợp lệ")
		return
	}
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, addrbook.ErrNotFound) {
			code = http.StatusNotFound
		} else if errors.Is(err, addrbook.ErrDuplicate) || errors.Is(err, addrbook.ErrLast) || errors.Is(err, addrbook.ErrFull) {
			code = http.StatusConflict
		}
		httpx.WriteError(w, code, err.Error())
		return
	}
	a.d.Log.Warn("admin sửa danh sách địa chỉ gửi cho agent", "thao_tac", req.Action, "dia_chi", req.URL,
		"ep_go", req.Force, "boi", actor(r), "danh_sach", b.URLs())
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "stats": b.Snapshot()})
}

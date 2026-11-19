package app

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/thangkes/SCS-System/common/fingerprint"
	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// Duyệt và thay thế máy cũ (cài lại máy): máy chờ duyệt trùng hostname hoặc vị trí TN với một máy đang hoạt động
// (khóa mới vì cài lại hệ điều hành) thì web gợi ý thay thế. Một thao tác: thu hồi máy cũ, duyệt máy mới, chuyển nhóm
// thử và cấu hình riêng theo máy, ghi máy mới "thay thế cho" máy cũ để xem lại lịch sử. Mức Duyệt agent (3) trở lên,
// như duyệt thường; duyệt tự động KHÔNG bao giờ tự thay thế (thay danh tính máy phải có người xác nhận).

// ReplaceCandidate: máy đang hoạt động mà máy chờ duyệt có thể thay thế, kèm so sánh phần cứng.
type ReplaceCandidate struct {
	ID       int64    `json:"id"`
	Hostname string   `json:"hostname"`
	LastSeen string   `json:"last_seen,omitempty"` // RFC 3339
	Hardware string   `json:"hardware"`            // same | partial | different | unknown
	Same     []string `json:"same,omitempty"`      // thành phần giống
	Diff     []string `json:"diff,omitempty"`      // thành phần khác
}

// CompareHardware so serial mainboard, serial hệ thống, MAC chính (machine-id luôn đổi khi cài lại nên không xét).
// same: mọi thành phần có ở cả hai đều giống; different: không thành phần nào giống; unknown: không có gì để so.
func CompareHardware(newC, oldC fingerprint.Components) (verdict string, same, diff []string) {
	a, b := newC.Normalize(), oldC.Normalize()
	for _, x := range []struct{ name, n, o string }{
		{"serial mainboard", a.BoardSerial, b.BoardSerial},
		{"serial hệ thống", a.SystemSerial, b.SystemSerial},
		{"MAC card mạng chính", strings.ToLower(a.PrimaryMAC), strings.ToLower(b.PrimaryMAC)},
	} {
		if x.n == "" || x.o == "" {
			continue
		}
		if x.n == x.o {
			same = append(same, x.name)
		} else {
			diff = append(diff, x.name)
		}
	}
	switch {
	case len(same) == 0 && len(diff) == 0:
		verdict = "unknown"
	case len(diff) == 0:
		verdict = "same"
	case len(same) == 0:
		verdict = "different"
	default:
		verdict = "partial"
	}
	return verdict, same, diff
}

// replace: POST /admin/v1/devices/{id}/replace {"old_id": …, "store_code": "…"} — duyệt máy {id} và thu hồi máy cũ.
func (a deviceAPI) replace(w http.ResponseWriter, r *http.Request) {
	id, ok := a.id(w, r)
	if !ok {
		return
	}
	var body struct {
		OldID     int64  `json:"old_id"`
		StoreCode string `json:"store_code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil || body.OldID <= 0 || body.OldID == id {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ: cần old_id của máy đang hoạt động cần thay")
		return
	}
	if body.StoreCode != "" && !storeCodeRe.MatchString(body.StoreCode) {
		httpx.WriteError(w, http.StatusBadRequest, ErrInvalidStore.Error())
		return
	}
	by := actor(r)
	dev, err := a.d.Registry.ReplaceApprove(r.Context(), id, body.OldID, by, body.StoreCode)
	if a.fail(w, err, "duyệt và thay thế máy") {
		return
	}
	a.d.Log.Warn("đã duyệt máy mới thay thế máy cũ (cài lại máy)", "by", by, "may_moi", id, "may_cu", body.OldID,
		"hostname", dev.Hostname, "sieu_thi", dev.StoreCode)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "device": dev})
}

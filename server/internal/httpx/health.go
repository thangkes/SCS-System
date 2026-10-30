package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Check là một kiểm tra sẵn sàng (DB, NATS...). Trả lỗi nghĩa là chưa sẵn sàng.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// ReadyCacheTTL: /readyz trả lại kết quả kiểm gần nhất trong khoảng này thay vì
// hỏi DB mỗi lần. Cổng agent mở ra Internet, nếu không có lưu tạm thì gọi /readyz
// dồn dập sẽ chiếm hết pool kết nối DB. Test đặt 0 để luôn kiểm lại.
var ReadyCacheTTL = 2 * time.Second

// MountHealth gắn hai endpoint:
//
//	GET /healthz  tiến trình còn sống (không gọi phụ thuộc nào)
//	GET /readyz   sẵn sàng nhận việc (chạy các Check; lỗi → 503 để LB bỏ node này ra)
//
// version rỗng thì không trả phiên bản (cổng công khai không để lộ phiên bản).
// /readyz chạy tối đa một lượt kiểm tại một thời điểm, kết quả dùng lại trong ReadyCacheTTL.
// Chi tiết lỗi chỉ ghi vào log, và chỉ ghi khi trạng thái đổi (ok → lỗi, lỗi → ok)
// để load balancer gọi mỗi vài giây không làm đầy log khi DB đang chết.
func MountHealth(mux *http.ServeMux, role, version string, checks []Check, log *slog.Logger) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{"status": "ok", "role": role}
		if version != "" {
			body["version"] = version
		}
		WriteJSON(w, http.StatusOK, body)
	})

	var (
		mu       sync.Mutex
		last     = map[string]string{} // tên check → lỗi lần trước ("" = ok)
		cachedAt time.Time
		cached   struct {
			code int
			body map[string]any
		}
	)
	check := func() (int, map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		if !cachedAt.IsZero() && time.Since(cachedAt) < ReadyCacheTTL {
			return cached.code, cached.body
		}
		// Không dùng context của request: request bị hủy giữa chừng không được
		// làm kết quả lưu tạm thành "lỗi".
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		status, code := "ok", http.StatusOK
		results := map[string]string{}
		for _, c := range checks {
			err := c.Fn(ctx)
			msg := ""
			if err != nil {
				msg = err.Error()
				results[c.Name] = "lỗi"
				status, code = "not_ready", http.StatusServiceUnavailable
			} else {
				results[c.Name] = "ok"
			}
			prev, seen := last[c.Name]
			last[c.Name] = msg
			switch {
			case msg != "" && (!seen || prev == ""):
				log.Warn("phụ thuộc chưa sẵn sàng", "check", c.Name, "err", msg)
			case msg == "" && seen && prev != "":
				log.Info("phụ thuộc đã sẵn sàng trở lại", "check", c.Name)
			}
		}
		cachedAt = time.Now()
		cached.code, cached.body = code, map[string]any{"status": status, "role": role, "checks": results}
		return cached.code, cached.body
	}
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		code, body := check() // ghi trả lời ngoài khóa: client chậm không chặn người khác
		WriteJSON(w, code, body)
	})
}

// WriteJSON trả JSON với mã trạng thái cho trước.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// WriteError trả lỗi dạng {"ok":false,"error":"..."}.
func WriteError(w http.ResponseWriter, code int, msg string) {
	WriteJSON(w, code, map[string]any{"ok": false, "error": msg})
}

// NotImplemented dùng cho route đã khai báo nhưng chưa làm, ghi rõ mốc sẽ làm.
func NotImplemented(milestone string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, http.StatusNotImplemented, "chưa làm (mốc "+milestone+")")
	}
}

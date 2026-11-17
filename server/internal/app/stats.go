package app

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// dbSizer: kho máy đo được dung lượng DB (PostgreSQL).
type dbSizer interface {
	DBSize(ctx context.Context) (int64, error)
}

// statsAPI: GET /admin/v1/server/stats — số liệu máy server, mạng, hoạt động SCS (chỉ đọc).
// Dung lượng DB hỏi tối đa mỗi phút một lần.
func statsAPI(d Deps) func(http.ResponseWriter, *http.Request) {
	var mu sync.Mutex
	var dbSize int64 = -1
	var dbAt time.Time
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Stats == nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, "tiến trình này không thu số liệu server")
			return
		}
		mu.Lock()
		if sz, ok := d.Registry.(dbSizer); ok && time.Since(dbAt) > time.Minute {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			if n, err := sz.DBSize(ctx); err == nil {
				dbSize = n
			}
			cancel()
			dbAt = time.Now()
		}
		size := dbSize
		mu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "stats": d.Stats.Snapshot(), "db_size": size})
	}
}

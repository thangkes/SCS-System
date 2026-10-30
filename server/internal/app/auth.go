package app

import (
	"net/http"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// requireClientCert chỉ cho qua request có chứng chỉ client đã được kiểm với CA.
//
// Ngoại lệ duy nhất: chưa khai SCS_TLS_CLIENT_CA và cấu hình cho phép rõ ràng
// (dev + chỉ nghe loopback, hoặc SCS_DEV_INSECURE=1). Mọi trường hợp khác thiếu CA
// đều bị chặn 401: quên cấu hình thì đóng chứ không mở.
func requireClientCert(d Deps) func(http.Handler) http.Handler {
	devBypass := d.Cfg.TLSClientCA == "" && d.Cfg.AllowInsecureIngest()
	return func(next http.Handler) http.Handler {
		if devBypass {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
				httpx.WriteError(w, http.StatusUnauthorized, "cần chứng chỉ client hợp lệ")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

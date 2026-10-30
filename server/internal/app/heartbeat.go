package app

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/pki"
)

// heartbeatEvery: nhịp agent gọi về (agent tự cộng trừ ngẫu nhiên để không dồn cùng lúc).
const heartbeatEvery = 5 * time.Minute

// heartbeatHandler xử lý POST /v1/heartbeat của máy đã duyệt (requireClientCert đã kiểm
// chứng chỉ do CA ký). Kiểm thêm: chứng chỉ đúng máy và là chứng chỉ mới nhất đã cấp, gói tin
// ký bằng khóa của máy, giờ không lệch. Trả nhịp gọi tiếp và danh sách địa chỉ server.
//
// Bản này chỉ ghi lần cuối thấy; lưu trạng thái máy (MachineStatus) ở mốc M4.
func heartbeatHandler(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.Registry == nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, "server chưa sẵn sàng")
			return
		}
		var cert *x509.Certificate
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			cert = r.TLS.PeerCertificates[0]
		}
		if cert == nil {
			httpx.WriteError(w, http.StatusUnauthorized, "cần chứng chỉ client hợp lệ")
			return
		}
		id, err := strconv.ParseInt(cert.Subject.CommonName, 10, 64)
		if err != nil || id <= 0 {
			httpx.WriteError(w, http.StatusUnauthorized, "chứng chỉ không mang mã máy")
			return
		}

		var s protocol.Signed
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "gói tin không đọc được")
			return
		}
		env, err := s.ParseUnverified()
		if err != nil || env.V != protocol.Version || env.Type != protocol.TypeHeartbeat {
			httpx.WriteError(w, http.StatusBadRequest, "gói tin sai định dạng")
			return
		}
		if env.DeviceID != cert.Subject.CommonName {
			httpx.WriteError(w, http.StatusForbidden, "mã máy trong gói khác chứng chỉ")
			return
		}
		if skew := time.Since(env.TS); skew > maxClockSkew || skew < -maxClockSkew {
			httpx.WriteError(w, http.StatusBadRequest, "giờ trong gói lệch quá 5 phút so với server")
			return
		}
		if !cleanText(env.AgentVersion, 64) {
			httpx.WriteError(w, http.StatusBadRequest, "phiên bản không hợp lệ")
			return
		}

		a, err := d.Registry.AgentAuth(r.Context(), id)
		switch {
		case errors.Is(err, ErrNotFound):
			httpx.WriteError(w, http.StatusForbidden, "máy không có trong danh sách")
			return
		case err != nil:
			d.Log.Error("đọc thông tin máy thất bại", "err", err, "device", id)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		if a.Status != DeviceApproved {
			httpx.WriteError(w, http.StatusForbidden, "máy đã bị thu hồi hoặc chưa được duyệt")
			return
		}
		if a.CertSerial == "" || pki.SerialHex(cert.SerialNumber) != a.CertSerial {
			httpx.WriteError(w, http.StatusForbidden, "chứng chỉ đã được thay bằng chứng chỉ mới hơn")
			return
		}
		if err := s.Verify(a.PublicKey); err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "chữ ký không hợp lệ")
			return
		}
		var body protocol.HeartbeatBody
		if err := env.DecodeBody(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "nội dung heartbeat sai định dạng")
			return
		}

		if err := d.Registry.Seen(r.Context(), id, remoteAddr(r), env.AgentVersion); err != nil {
			d.Log.Error("ghi heartbeat thất bại", "err", err, "device", id)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, protocol.Response{
			OK: true, NextPollS: int(heartbeatEvery / time.Second), ServerTime: time.Now().UTC(),
			Servers: d.Cfg.AgentServers,
		})
	})
}

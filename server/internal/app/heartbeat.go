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

// heartbeatEvery: nhịp tối thiểu agent gọi về. Agent chờ ngẫu nhiên từ nhịp này tới gấp đôi
// (5–10 phút) để không dồn cùng lúc; agent bản cũ chờ 5 phút ±20%.
const heartbeatEvery = 5 * time.Minute

// authDevice: máy gọi bằng chứng chỉ client (requireClientCert đã kiểm do CA ký) phải còn được
// duyệt và chứng chỉ phải là chứng chỉ mới nhất đã cấp. Trả mã máy; lỗi thì đã trả lời.
func authDevice(d Deps, w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, _, ok := authDeviceKey(d, w, r)
	return id, ok
}

func authDeviceKey(d Deps, w http.ResponseWriter, r *http.Request) (int64, AgentAuth, bool) {
	if d.Registry == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "server chưa sẵn sàng")
		return 0, AgentAuth{}, false
	}
	var cert *x509.Certificate
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		cert = r.TLS.PeerCertificates[0]
	}
	if cert == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "cần chứng chỉ client hợp lệ")
		return 0, AgentAuth{}, false
	}
	id, err := strconv.ParseInt(cert.Subject.CommonName, 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusUnauthorized, "chứng chỉ không mang mã máy")
		return 0, AgentAuth{}, false
	}
	a, err := d.Registry.AgentAuth(r.Context(), id)
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, http.StatusForbidden, "máy không có trong danh sách")
		return 0, a, false
	case err != nil:
		d.Log.Error("đọc thông tin máy thất bại", "err", err, "device", id)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return 0, a, false
	}
	if a.Status != DeviceApproved {
		httpx.WriteError(w, http.StatusForbidden, "máy đã bị thu hồi hoặc chưa được duyệt")
		return 0, a, false
	}
	if a.CertSerial == "" || pki.SerialHex(cert.SerialNumber) != a.CertSerial {
		httpx.WriteError(w, http.StatusForbidden, "chứng chỉ đã được thay bằng chứng chỉ mới hơn")
		return 0, a, false
	}
	return id, a, true
}

// heartbeatHandler xử lý POST /v1/heartbeat của máy đã duyệt. Ngoài authDevice còn kiểm gói tin
// ký bằng khóa của máy, đúng mã máy, giờ không lệch. Trả nhịp gọi tiếp, danh sách địa chỉ server,
// bản agent máy cần chạy (theo nhóm phát hành) và cấu hình agent (khi khác bản agent đang dùng).
//
// Ghi trạng thái mới nhất của máy (một dòng mỗi máy), lịch sử khi trạng thái đổi đáng kể, và giai
// đoạn của máy (docs/thiet-ke-luu-theo-giai-doan.md).
// readAgentPacket đọc và kiểm một gói tin đã ký của máy đã duyệt: đúng loại, giờ không lệch, chứng chỉ
// client còn hiệu lực, đúng mã máy, chữ ký bằng khóa của máy. Lỗi thì đã trả lời.
func readAgentPacket(d Deps, w http.ResponseWriter, r *http.Request, typ string) (protocol.Envelope, int64, bool) {
	var s protocol.Signed
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "gói tin không đọc được")
		return protocol.Envelope{}, 0, false
	}
	env, err := s.ParseUnverified()
	if err != nil || env.V != protocol.Version || env.Type != typ {
		httpx.WriteError(w, http.StatusBadRequest, "gói tin sai định dạng")
		return env, 0, false
	}
	if skew := time.Since(env.TS); skew > maxClockSkew || skew < -maxClockSkew {
		httpx.WriteError(w, http.StatusBadRequest, "giờ trong gói lệch quá 5 phút so với server")
		return env, 0, false
	}
	if !cleanText(env.AgentVersion, 64) {
		httpx.WriteError(w, http.StatusBadRequest, "phiên bản không hợp lệ")
		return env, 0, false
	}
	id, a, ok := authDeviceKey(d, w, r)
	if !ok {
		return env, 0, false
	}
	if env.DeviceID != strconv.FormatInt(id, 10) {
		httpx.WriteError(w, http.StatusForbidden, "mã máy trong gói khác chứng chỉ")
		return env, 0, false
	}
	if err := s.Verify(a.PublicKey); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "chữ ký không hợp lệ")
		return env, 0, false
	}
	return env, id, true
}

func heartbeatHandler(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env, id, ok := readAgentPacket(d, w, r, protocol.TypeHeartbeat)
		if !ok {
			return
		}
		var body protocol.HeartbeatBody
		if err := env.DecodeBody(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "nội dung heartbeat sai định dạng")
			return
		}

		if !cleanText(body.RuleSetVersion, 64) {
			body.RuleSetVersion = ""
		}
		if err := d.Registry.RecordHeartbeat(r.Context(), id, heartbeatRecord(env, body, remoteAddr(r))); err != nil {
			d.Log.Error("ghi heartbeat thất bại", "err", err, "device", id)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		resp := protocol.Response{
			OK: true, NextPollS: int(heartbeatEvery / time.Second), ServerTime: time.Now().UTC(),
			Servers: agentServers(d),
		}
		d.Servers.Report(id, body.ServersHash, body.ServerURL)
		// Bản agent và cấu hình: đọc lỗi thì heartbeat vẫn thành công, lần sau gửi.
		arch := body.Arch
		if !protocol.ValidArch(arch) {
			arch = ""
		}
		if plan, err := d.Registry.AgentPlan(r.Context(), id, arch); err != nil {
			d.Log.Error("đọc bản agent / cấu hình cho máy thất bại", "err", err, "device", id)
		} else {
			if rel := plan.Release; rel != nil && rel.Version != env.AgentVersion {
				resp.Update = &protocol.UpdateInfo{Version: rel.Version, Arch: rel.Arch, SHA256: rel.SHA256,
					Size: rel.Size, Sig: rel.Sig}
			}
			if v := plan.RuleSet; v != "" && d.Rules != nil && hasCap(body.Capabilities, protocol.CapRulesPrefix) {
				if _, err := ruleBundleFor(d, v); err == nil {
					resp.RuleSetVersion = v
				}
			}
			// Chưa đặt cấu hình ở cấp nào thì không gửi: agent giữ cấu hình đang có.
			if plan.WatchServices != nil || plan.Labels != nil {
				if c := configOf(plan.WatchServices, plan.Labels); c.Version != body.ConfigVersion {
					resp.Config = &c
				}
			}
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	})
}

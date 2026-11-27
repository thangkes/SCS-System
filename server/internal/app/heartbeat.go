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

// heartbeatEvery / heartbeatMax: agent chờ ngẫu nhiên (theo giây) trong khoảng 5–30 phút rồi mới gọi
// tiếp, để hàng nghìn máy không dồn cùng lúc và giảm tải server. Agent bản cũ không đọc
// next_poll_max_s nên vẫn chờ 5–10 phút tới khi được cập nhật. SCS_OFFLINE_AFTER phải ≥ 2 × heartbeatMax.
const (
	heartbeatEvery = 5 * time.Minute
	heartbeatMax   = 30 * time.Minute
)

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
	if !validNonce(env.Nonce) || !replays.fresh(env.DeviceID, env.Nonce, time.Now()) {
		d.Log.Warn("gói gửi lại (nonce trùng) hoặc nonce sai: từ chối", "device", id, "loai", typ, "ip", remoteAddr(r))
		httpx.WriteError(w, http.StatusConflict, "gói đã được nhận trước đó (gửi lại) — bỏ qua")
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
		body.Status = cleanStatus(body.Status)
		rec := heartbeatRecord(env, body, remoteAddr(r))
		if body.Status != nil {
			h := evalHealth(body.Status, lowMemStreak(id, memIsLow(body.Status)))
			rec.Health = &h
		}
		if err := d.Registry.RecordHeartbeat(r.Context(), id, rec); err != nil {
			d.Log.Error("ghi heartbeat thất bại", "err", err, "device", id)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		activity(d).Heartbeat()
		if body.LAN != nil {
			ingestLAN(r.Context(), d, id, body.LAN)
		}
		resp := protocol.Response{
			OK: true, NextPollS: int(heartbeatEvery / time.Second), NextPollMaxS: int(heartbeatMax / time.Second), ServerTime: time.Now().UTC(),
			Servers: agentServers(d),
		}
		d.Servers.Report(id, body.ServersHash, body.ServerURL)
		if u := body.UpdateError; u != nil && protocol.ValidVersion(u.Version) && u.Version != env.AgentVersion {
			msg := u.Error
			if len(msg) > 200 || !cleanText(msg, 200) {
				msg = "lỗi không đọc được"
			}
			d.Downloads.Report(id, &UpdateFailure{Version: u.Version, Error: msg, At: time.Now().UTC()})
		} else {
			d.Downloads.Report(id, nil)
		}
		// Bản agent và cấu hình: đọc lỗi thì heartbeat vẫn thành công, lần sau gửi.
		arch := body.Arch
		if !protocol.ValidArch(arch) {
			arch = ""
		}
		if plan, err := d.Registry.AgentPlan(r.Context(), id, arch); err != nil {
			d.Log.Error("đọc bản agent / cấu hình cho máy thất bại", "err", err, "device", id)
		} else {
			// Mọi lần tải (bản agent, bộ rule; máy cũ lẫn máy mới hòa mạng) phải giữ một trong các lượt
			// tải (Downloads, mặc định 4): chỉ báo bản / bộ mới cho máy giữ được lượt; hết lượt thì máy chạy
			// tiếp cái đang có và xin lại ở heartbeat sau. Một lượt dùng cho cả bộ rule và bản agent.
			rel := plan.Release
			needUpdate := rel != nil && rel.Version != env.AgentVersion
			// Máy đang báo lỗi với chính bản này thì không giữ lượt ở heartbeat (agent tự thử lại sau 1 giờ;
			// lúc tải vẫn phải xin lượt ở cổng tải).
			failing := needUpdate && body.UpdateError != nil && body.UpdateError.Version == rel.Version
			ruleV := ""
			if v := plan.RuleSet; v != "" && d.Rules != nil && hasCap(body.Capabilities, protocol.CapRulesPrefix) {
				// Chỉ phát bộ agent đọc được: agent cũ gặp schema mới sẽ từ chối, tải lại mãi và tốn lượt tải.
				if b, err := ruleBundleFor(d, v); err == nil && bundleSchema(b) <= capRules(body.Capabilities) {
					ruleV = v
				}
			}
			needRules := ruleV != "" && !hasRuleSet(body, ruleV)
			slot := false
			if (needUpdate && !failing) || needRules {
				slot = d.Downloads.Acquire(id)
			}
			if needUpdate && (failing || slot) {
				resp.Update = &protocol.UpdateInfo{Version: rel.Version, Arch: rel.Arch, SHA256: rel.SHA256,
					Size: rel.Size, Sig: rel.Sig}
			}
			if ruleV != "" && (!needRules || slot) {
				resp.RuleSetVersion = ruleV
			}
			// Chưa đặt cấu hình ở cấp nào thì không gửi: agent giữ cấu hình đang có.
			if plan.WatchServices != nil || plan.Labels != nil {
				if c := configOf(plan.WatchServices, plan.Labels); c.Version != body.ConfigVersion {
					resp.Config = &c
				}
			}
		}
		resp.AlertSeverity, _ = d.Registry.AlertLevel(r.Context(), id)
		nudgeLive() // máy gửi lại sau mất liên lạc / máy mới chờ duyệt: web biết ngay
		httpx.WriteJSON(w, http.StatusOK, resp)
	})
}

// hasRuleSet: máy đã cài đúng bộ rule v (không cần tải). Agent mới báo trong Rules; agent cũ chỉ có
// RuleSetVersion.
func hasRuleSet(body protocol.HeartbeatBody, v string) bool {
	if r := body.Rules; r != nil {
		return r.Installed && r.Name+"@"+r.Version == v
	}
	return body.RuleSetVersion == v
}

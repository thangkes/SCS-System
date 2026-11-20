package app

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/common/fleet"
	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// maxClockSkew: giờ trong gói lệch giờ server quá ngần này thì từ chối (chống gửi lại gói cũ).
// Agent tự bù lệch theo header Date của server rồi gửi lại.
const maxClockSkew = 5 * time.Minute

// enrollHandler xử lý POST /v1/enroll. Chưa cần chứng chỉ client: đây là bước máy mới xin vào.
//
// Thứ tự kiểm (rẻ trước, đắt sau): giới hạn theo IP → định dạng gói → giờ → chữ ký bằng
// chính khóa trong gói → fingerprint khớp các thành phần → hostname → ghi DB.
// Máy đã được duyệt hỏi lại thì được cấp chứng chỉ mới (serial cũ hết hiệu lực).
func enrollHandler(d Deps) http.Handler {
	lim := newEnrollLimiter(time.Now)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.Registry == nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, "server chưa sẵn sàng nhận đăng ký")
			return
		}
		ip := remoteAddr(r)
		if !lim.allow(ip) {
			w.Header().Set("Retry-After", "600")
			httpx.WriteError(w, http.StatusTooManyRequests, "gửi đăng ký quá nhiều, thử lại sau")
			return
		}

		var s protocol.Signed
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "gói tin không đọc được")
			return
		}
		env, err := s.ParseUnverified()
		if err != nil || env.V != protocol.Version || env.Type != protocol.TypeEnroll {
			httpx.WriteError(w, http.StatusBadRequest, "gói tin sai định dạng")
			return
		}
		if skew := time.Since(env.TS); skew > maxClockSkew || skew < -maxClockSkew {
			httpx.WriteError(w, http.StatusBadRequest, "giờ trong gói lệch quá 5 phút so với server")
			return
		}
		var body protocol.EnrollBody
		if err := env.DecodeBody(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "nội dung đăng ký sai định dạng")
			return
		}
		pub, err := protocol.DecodePublicKey(body.PublicKey)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "khóa công khai không hợp lệ")
			return
		}
		if err := s.Verify(pub); err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "chữ ký không hợp lệ")
			return
		}
		// Chống gửi lại gói đăng ký / gia hạn (gửi lại gói gia hạn sẽ làm server cấp chứng chỉ mới cho máy).
		if !validNonce(env.Nonce) || !replays.fresh("key:"+body.PublicKey, env.Nonce, time.Now()) {
			httpx.WriteError(w, http.StatusConflict, "gói đã được nhận trước đó (gửi lại) — bỏ qua")
			return
		}
		fp := body.Components.Hash()
		if env.Fingerprint != fp {
			httpx.WriteError(w, http.StatusBadRequest, "fingerprint không khớp các thành phần")
			return
		}
		if !cleanText(body.Hostname, 64) || body.Hostname == "" || !cleanText(body.OSVersion, 128) || !cleanText(env.AgentVersion, 64) {
			httpx.WriteError(w, http.StatusBadRequest, "hostname / phiên bản không hợp lệ")
			return
		}
		comps, _ := json.Marshal(body.Components.Normalize())

		req := EnrollRequest{
			PublicKey: pub, Fingerprint: fp, Components: comps, Hostname: body.Hostname,
			OSVersion: body.OSVersion, AgentVersion: env.AgentVersion, IP: ip,
		}
		if n, err := fleet.ParseHostname(body.Hostname); err == nil {
			req.HostnameOK, req.StoreCode, req.TN, req.Role = true, n.Store, n.TN, string(n.Role)
		} else {
			req.HostnameError = err.Error()
		}

		res, err := d.Registry.Enroll(r.Context(), req)
		switch {
		case errors.Is(err, ErrTooManyPending):
			w.Header().Set("Retry-After", "3600")
			httpx.WriteError(w, http.StatusServiceUnavailable, "server đang có quá nhiều yêu cầu chờ duyệt")
			return
		case err != nil:
			d.Log.Error("ghi đăng ký thất bại", "err", err, "hostname", body.Hostname)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		if res.New {
			d.Log.Info("máy mới xin đăng ký, chờ duyệt", "hostname", body.Hostname, "ip", ip.String(),
				"device", res.DeviceID, "ma_khoa", KeyID(pub), "hostname_dung", req.HostnameOK)
		}

		// Duyệt tự động (công tắc trên trang Chờ duyệt): kiểm mỗi lần máy đang chờ hỏi lại.
		// Lỗi khi kiểm không chặn đăng ký: máy vẫn chờ người duyệt như cũ.
		if res.Status == DevicePending {
			ar, err := d.Registry.AutoApprove(r.Context(), res.DeviceID, ip)
			switch {
			case err != nil:
				d.Log.Warn("kiểm duyệt tự động thất bại, máy vẫn chờ duyệt", "err", err, "device", res.DeviceID)
			case ar.Approved:
				res.Status = DeviceApproved
				d.Log.Info("đã duyệt tự động máy", "device", res.DeviceID, "hostname", body.Hostname,
					"sieu_thi", ar.Store, "ip", ip.String())
			case res.New && !ar.Off:
				d.Log.Info("chưa duyệt tự động được, chờ người duyệt", "device", res.DeviceID, "ly_do", ar.Reason)
			}
		}

		resp := protocol.EnrollResponse{Status: protocol.EnrollPending}
		// Máy mới hòa mạng cũng phải chờ lượt tải (như bản agent, bộ rule): hết lượt thì vẫn trả "chờ duyệt",
		// agent hỏi lại sau ~5 phút. Chấp nhận hòa mạng chậm hơn để tải của server luôn trong giới hạn.
		if res.Status == DeviceApproved && !d.Downloads.Acquire(res.DeviceID) {
			httpx.WriteJSON(w, http.StatusOK, resp)
			return
		}
		switch res.Status {
		case DeviceApproved:
			defer d.Downloads.Free(res.DeviceID)
			if d.CA == nil {
				httpx.WriteError(w, http.StatusServiceUnavailable, "server chưa cấu hình CA để cấp chứng chỉ")
				return
			}
			id := strconv.FormatInt(res.DeviceID, 10)
			iss, err := d.CA.IssueClient(pub, id)
			if err == nil {
				err = d.Registry.SetCertSerial(r.Context(), res.DeviceID, iss.Serial, iss.NotAfter)
			}
			if err != nil {
				d.Log.Error("cấp chứng chỉ thất bại", "err", err, "device", res.DeviceID)
				httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
				return
			}
			d.Log.Info("đã cấp chứng chỉ cho máy", "device", res.DeviceID, "hostname", body.Hostname,
				"serial", iss.Serial, "het_han", iss.NotAfter.Format(time.DateOnly))
			resp = protocol.EnrollResponse{Status: protocol.EnrollApproved, DeviceID: id,
				CertificatePEM: string(iss.PEM), CAPEM: string(d.CA.CertPEM), Servers: agentServers(d)}
		case DeviceRejected, DeviceRevoked:
			resp.Status = protocol.EnrollRejected
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	})
}

// cleanText: chuỗi do agent gửi, giới hạn độ dài, không có ký tự điều khiển / vô hình.
func cleanText(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Co) {
			return false
		}
	}
	return true
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, _ := netip.ParseAddr(host)
	return a.Unmap()
}

// enrollLimiter: mỗi IP tối đa enrollBurst lần liền, sau đó 1 lần mỗi enrollEvery.
// Một siêu thị 5 máy đang chờ duyệt hỏi lại mỗi 5 phút ≈ 1 lần/phút, dư sức.
type enrollLimiter struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[netip.Addr]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	enrollBurst = 20
	enrollEvery = 20 * time.Second
	enrollMaxIP = 50000
)

func newEnrollLimiter(now func() time.Time) *enrollLimiter {
	return &enrollLimiter{now: now, m: map[netip.Addr]*bucket{}}
}

func (l *enrollLimiter) allow(ip netip.Addr) bool {
	if ip.Is6() {
		ip = netip.PrefixFrom(ip, 64).Masked().Addr() // IPv6 tính theo dải /64
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.m[ip]
	if b == nil {
		if len(l.m) >= enrollMaxIP {
			for k, v := range l.m { // dọn IP đã hồi đầy lượt
				if now.Sub(v.last) > enrollBurst*enrollEvery {
					delete(l.m, k)
				}
			}
		}
		b = &bucket{tokens: enrollBurst, last: now}
		l.m[ip] = b
	}
	b.tokens = min(enrollBurst, b.tokens+now.Sub(b.last).Seconds()/enrollEvery.Seconds())
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

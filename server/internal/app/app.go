// Package app ghép route cho từng vai trò của scs-server.
// Route chưa làm trả 501 kèm mốc sẽ làm.
package app

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/addrbook"
	"github.com/thangkes/SCS-System/server/internal/auth"
	"github.com/thangkes/SCS-System/server/internal/cluster"
	"github.com/thangkes/SCS-System/server/internal/config"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/pki"
	"github.com/thangkes/SCS-System/server/internal/web"
)

// Deps là những thứ các handler dùng chung.
type Deps struct {
	Cfg         *config.Config
	Log         *slog.Logger
	Version     string
	ReadyChecks []httpx.Check // chạy ở /readyz (DB, sau này thêm NATS)
	Auth        *auth.Manager // đăng nhập web quản trị; chỉ cần cho AdminHandler
	Registry    Registry      // kho máy (PostgreSQL); nil = chưa sẵn sàng
	CA          *pki.CA       // CA cấp chứng chỉ agent; nil = chưa cấu hình SCS_CA_CERT/KEY
	// ReleaseKey ký bản agent server phát hành; nil = chưa có SCS_RELEASE_KEY (không phát hành).
	ReleaseKey ed25519.PrivateKey
	// Cluster: node của cụm nhiều server (nil = không chạy phần cụm trong tiến trình này).
	Cluster *cluster.Node
	// Servers: danh sách địa chỉ server gửi cho agent (nil = dùng SCS_AGENT_SERVERS, chỉ khi test).
	Servers *addrbook.Book
}

// agentServers: danh sách địa chỉ gửi cho agent ở enroll / heartbeat.
func agentServers(d Deps) []string {
	if d.Servers != nil {
		return d.Servers.URLs()
	}
	return d.Cfg.AgentServers
}

// Body tối đa: payload đã ký được mã hóa base64 (~4/3) cộng phần JSON bọc ngoài.
// Đây là trần chung; từng route trên cổng agent có giới hạn nhỏ hơn (ingestBody).
const maxRequestBody = protocol.MaxPayloadSize*2 + 4096

// Giới hạn body theo route trên cổng agent: heartbeat chỉ vài KB nên không cho gửi tới 2 MB.
// Request khai Content-Length vượt giới hạn bị trả 413 ngay, không đọc body.
var ingestBody = struct{ enroll, heartbeat, logs, renew, get int64 }{
	enroll:    16 << 10,
	heartbeat: 64 << 10,
	logs:      maxRequestBody,
	renew:     16 << 10,
	get:       1 << 10,
}

// Số request được xử lý cùng lúc trên cổng agent, chia ba nhóm để nhóm này bị dồn
// không chặn nhóm kia. Quá giới hạn thì trả 503 kèm next_poll_s cho agent giãn nhịp.
// RAM tối đa cho body ≈ 32×16 KB + 512×64 KB + 64×2 MB ≈ 160 MB.
var ingestInflight = struct{ enroll, agent, logs int }{
	enroll: 32,  // chưa xác thực: bị dò dồn dập cũng chỉ chiếm nhóm này
	agent:  512, // heartbeat, gia hạn chứng chỉ, tải rule/cập nhật
	logs:   64,  // body lớn nhất
}

// IngestHandler: toàn bộ API cho agent trên một cổng (mặc định 6222), để cửa hàng chỉ mở một cổng.
//
// TLS chấp nhận kết nối không có chứng chỉ client (máy mới chưa có chứng chỉ để đăng ký),
// nhưng chứng chỉ không do CA ký vẫn bị từ chối ngay lúc bắt tay. Mọi route trừ
// /v1/enroll và health check đều bắt buộc chứng chỉ client đã kiểm.
//
// Thứ tự kiểm mỗi route: chứng chỉ → kích thước body → chỗ xử lý. Request không hợp lệ
// bị loại trước, không chiếm chỗ của agent thật.
func IngestHandler(d Deps) http.Handler {
	mux := http.NewServeMux()
	httpx.MountHealth(mux, "ingest", "", d.ReadyChecks, d.Log) // cổng công khai: không lộ phiên bản

	cert := requireClientCert(d)
	enroll := httpx.Limit("enroll", ingestInflight.enroll, d.Log)
	agent := httpx.Limit("agent", ingestInflight.agent, d.Log)
	logs := httpx.Limit("logs", ingestInflight.logs, d.Log)
	route := func(auth func(http.Handler) http.Handler, body int64, slot httpx.Middleware, h http.Handler) http.Handler {
		return auth(httpx.Chain(h, httpx.MaxBody(body), slot))
	}
	open := func(h http.Handler) http.Handler { return h }

	// Máy mới: chưa có chứng chỉ; mọi yêu cầu đăng ký chờ admin duyệt.
	mux.Handle("POST /v1/enroll", route(open, ingestBody.enroll, enroll, enrollHandler(d)))

	mux.Handle("POST /v1/heartbeat", route(cert, ingestBody.heartbeat, agent, heartbeatHandler(d)))
	mux.Handle("POST /v1/logs", route(cert, ingestBody.logs, logs, httpx.NotImplemented("M4")))
	mux.Handle("POST /v1/cert/renew", route(cert, ingestBody.renew, agent, httpx.NotImplemented("M3")))
	mux.Handle("GET /v1/rules/{version}", route(cert, ingestBody.get, agent, httpx.NotImplemented("M7")))
	mux.Handle("GET /v1/updates/{version}", route(cert, ingestBody.get, agent, updateHandler(d)))
	return wrap(d, mux, httpx.PublicAccessLog(d.Log))
}

// AdminHandler: web quản trị và API cho giao diện. Đăng nhập bằng mã 6 số
// (lệnh `scs-server login-code`); quyền Chỉ xem đọc được, mọi thao tác ghi cần Admin.
func AdminHandler(d Deps) http.Handler {
	mux := http.NewServeMux()
	httpx.MountHealth(mux, "admin", d.Version, d.ReadyChecks, d.Log)
	web.Mount(mux) // giao diện ở "/" và "/static/"
	d.Auth.Mount(mux)

	view, admin := d.Auth.Require(auth.RoleViewer), d.Auth.Require(auth.RoleAdmin)
	dev := deviceAPI{d}
	mux.Handle("GET /admin/v1/devices", view(http.HandlerFunc(dev.list)))
	mux.Handle("GET /admin/v1/devices/{id}", view(httpx.NotImplemented("M6")))
	mux.Handle("POST /admin/v1/devices/{id}/approve", admin(http.HandlerFunc(dev.approve)))
	mux.Handle("POST /admin/v1/devices/{id}/reject", admin(http.HandlerFunc(dev.reject)))
	mux.Handle("POST /admin/v1/devices/{id}/revoke", admin(http.HandlerFunc(dev.revoke)))
	mux.Handle("GET /admin/v1/alerts", view(http.HandlerFunc(dev.alerts)))
	mux.Handle("POST /admin/v1/alerts/{id}/ack", admin(http.HandlerFunc(dev.ackAlert)))
	ag := agentAPI{d}
	mux.Handle("GET /admin/v1/agent/releases", view(http.HandlerFunc(ag.releases)))
	mux.Handle("POST /admin/v1/agent/rollout", admin(http.HandlerFunc(ag.rollout)))
	mux.Handle("POST /admin/v1/devices/{id}/test-ring", admin(http.HandlerFunc(ag.testRing)))
	mux.Handle("GET /admin/v1/agent/config", view(http.HandlerFunc(ag.configs)))
	mux.Handle("POST /admin/v1/agent/config", admin(http.HandlerFunc(ag.setConfig)))
	mux.Handle("POST /admin/v1/agent/labels", admin(http.HandlerFunc(ag.setLabels)))
	sv := serversAPI{d}
	mux.Handle("GET /admin/v1/agent/servers", view(http.HandlerFunc(sv.list)))
	mux.Handle("POST /admin/v1/agent/servers", admin(http.HandlerFunc(sv.change)))
	cl := clusterAPI{d}
	mux.Handle("GET /admin/v1/cluster", view(http.HandlerFunc(cl.status)))
	mux.Handle("POST /admin/v1/cluster/approve", admin(http.HandlerFunc(cl.approve)))
	mux.Handle("POST /admin/v1/cluster/unapprove", admin(http.HandlerFunc(cl.unapprove)))
	mux.Handle("POST /admin/v1/cluster/reject-join", admin(http.HandlerFunc(cl.rejectJoin)))
	mux.Handle("POST /admin/v1/cluster/isolate-local", admin(http.HandlerFunc(cl.isolateLocal)))
	return wrap(d, httpx.RejectProxied(d.Log)(mux), httpx.AggregatedAccessLog(d.Log, slog.LevelInfo))
}

// Worker: xử lý hàng đợi. Bản khung chỉ chờ tới khi được yêu cầu dừng.
func Worker(d Deps) func(context.Context) error {
	return func(ctx context.Context) error {
		d.Log.Info("worker đang chạy, chưa có việc (mốc M5)")
		<-ctx.Done()
		return nil
	}
}

func wrap(d Deps, h http.Handler, accessLog httpx.Middleware) http.Handler {
	return httpx.Chain(h,
		httpx.WithRequestID,
		httpx.SecureHeaders,
		accessLog,
		httpx.Recover(d.Log),
		httpx.MaxBody(maxRequestBody),
	)
}

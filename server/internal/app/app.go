// Package app ghép route cho từng vai trò của scs-server.
// Route chưa làm trả 501 kèm mốc sẽ làm.
package app

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/addrbook"
	"github.com/thangkes/SCS-System/server/internal/auth"
	"github.com/thangkes/SCS-System/server/internal/cluster"
	"github.com/thangkes/SCS-System/server/internal/config"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/pki"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
	"github.com/thangkes/SCS-System/server/internal/sysstat"
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
	// Rules: kho bộ rule (file nguồn, bản đã ký); nil = chưa mở được (trang Rule báo lỗi).
	Rules *rulestore.Store
	// Live: thông báo tức thời cho web quản trị (nil = không có luồng, trang tự tải lại định kỳ).
	Live *Live
	// Stats: số liệu máy server và hoạt động cho trang Tổng quan (nil = không có).
	Stats *sysstat.Sampler
}

// activity: bộ đếm hoạt động của cổng agent (nil an toàn).
func activity(d Deps) *sysstat.Activity {
	if d.Stats == nil {
		return nil
	}
	return d.Stats.Act
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
	mux.Handle("POST /v1/logs", route(cert, ingestBody.logs, logs, logsHandler(d)))
	mux.Handle("POST /v1/cert/renew", route(cert, ingestBody.renew, agent, httpx.NotImplemented("M3")))
	mux.Handle("GET /v1/rules/{version}", route(cert, ingestBody.get, agent, rulesHandler(d)))
	mux.Handle("GET /v1/updates/{version}", route(cert, ingestBody.get, agent, updateHandler(d)))
	return wrap(d, mux, httpx.PublicAccessLog(d.Log))
}

// AdminHandler: web quản trị và API cho giao diện. Đăng nhập bằng mã 6 số lấy qua SSH (lệnh
// `scs-server login-code`: quản trị gốc / chỉ xem) hoặc tài khoản email (mức quyền 1–7, auth.Level).
// Mỗi thao tác ghi cần một mức quyền; mức cao hơn (số nhỏ hơn) làm được mọi việc của mức thấp hơn.
func AdminHandler(d Deps) http.Handler {
	mux := http.NewServeMux()
	httpx.MountHealth(mux, "admin", d.Version, d.ReadyChecks, d.Log)
	web.Mount(mux) // giao diện ở "/" và "/static/"
	d.Auth.Mount(mux)

	lv := d.Auth.RequireLevel
	view := lv(auth.LevelView)
	cluster, rules, agents := lv(auth.LevelCluster), lv(auth.LevelRules), lv(auth.LevelAgents)
	servers, incidents, agentInfo := lv(auth.LevelServers), lv(auth.LevelIncidents), lv(auth.LevelAgentInfo)
	dev := deviceAPI{d}
	mux.Handle("GET /admin/v1/devices", view(http.HandlerFunc(dev.list)))
	mux.Handle("GET /admin/v1/devices/{id}", view(httpx.NotImplemented("M6")))
	mux.Handle("GET /admin/v1/devices/{id}/periods", view(http.HandlerFunc(dev.timeline)))
	mux.Handle("POST /admin/v1/periods/{id}/resolve", incidents(http.HandlerFunc(dev.resolve)))
	mux.Handle("POST /admin/v1/devices/{id}/approve", agents(http.HandlerFunc(dev.approve)))
	mux.Handle("POST /admin/v1/devices/{id}/reject", agents(http.HandlerFunc(dev.reject)))
	mux.Handle("POST /admin/v1/devices/{id}/revoke", agents(http.HandlerFunc(dev.revoke)))
	mux.Handle("GET /admin/v1/alerts", view(http.HandlerFunc(dev.alerts)))
	mux.Handle("POST /admin/v1/alerts/{id}/ack", incidents(http.HandlerFunc(dev.ackAlert)))
	ag := agentAPI{d}
	mux.Handle("GET /admin/v1/agent/releases", view(http.HandlerFunc(ag.releases)))
	mux.Handle("POST /admin/v1/agent/rollout", agents(http.HandlerFunc(ag.rollout)))
	mux.Handle("POST /admin/v1/devices/{id}/test-ring", agentInfo(http.HandlerFunc(ag.testRing)))
	mux.Handle("GET /admin/v1/agent/config", view(http.HandlerFunc(ag.configs)))
	mux.Handle("POST /admin/v1/agent/config", agentInfo(http.HandlerFunc(ag.setConfig)))
	mux.Handle("POST /admin/v1/agent/labels", agentInfo(http.HandlerFunc(ag.setLabels)))
	sv := serversAPI{d}
	mux.Handle("GET /admin/v1/agent/servers", view(http.HandlerFunc(sv.list)))
	mux.Handle("POST /admin/v1/agent/servers", servers(http.HandlerFunc(sv.change)))
	cl := clusterAPI{d}
	mux.Handle("GET /admin/v1/cluster", view(http.HandlerFunc(cl.status)))
	mux.Handle("POST /admin/v1/cluster/approve", cluster(http.HandlerFunc(cl.approve)))
	mux.Handle("POST /admin/v1/cluster/unapprove", cluster(http.HandlerFunc(cl.unapprove)))
	mux.Handle("POST /admin/v1/cluster/reject-join", cluster(http.HandlerFunc(cl.rejectJoin)))
	mux.Handle("POST /admin/v1/cluster/isolate-local", cluster(http.HandlerFunc(cl.isolateLocal)))
	if d.Live != nil {
		mux.Handle("GET /admin/v1/stream", view(d.Live.Handler(d.Auth)))
	} else {
		mux.Handle("GET /admin/v1/stream", view(httpx.NotImplemented("thông báo tức thời")))
	}
	mux.Handle("GET /admin/v1/server/stats", view(http.HandlerFunc(statsAPI(d))))
	st := storesAPI{d}
	mux.Handle("GET /admin/v1/stores", view(http.HandlerFunc(st.list)))
	mux.Handle("POST /admin/v1/stores/{code}", agentInfo(http.HandlerFunc(st.save)))
	ru := rulesAPI{d}
	mux.Handle("GET /admin/v1/rules", view(http.HandlerFunc(ru.list)))
	mux.Handle("GET /admin/v1/rules/source", view(http.HandlerFunc(ru.source)))
	mux.Handle("GET /admin/v1/rules/bundle", view(http.HandlerFunc(ru.bundle)))
	mux.Handle("POST /admin/v1/rules", rules(http.HandlerFunc(ru.add)))
	mux.Handle("POST /admin/v1/rules/approve", rules(http.HandlerFunc(ru.approve)))
	mux.Handle("POST /admin/v1/rules/remove", rules(http.HandlerFunc(ru.remove)))
	mux.Handle("GET /admin/v1/rules/assign", view(http.HandlerFunc(ru.assignments)))
	mux.Handle("POST /admin/v1/rules/assign", rules(http.HandlerFunc(ru.assign)))
	return wrap(d, httpx.RejectProxied(d.Log)(mux), httpx.AggregatedAccessLog(d.Log, slog.LevelInfo))
}

// periodTickEvery: nhịp kiểm mất liên lạc / tự đóng cảnh báo high.
const periodTickEvery = time.Minute

// Worker: việc định kỳ. Hiện có: kiểm giai đoạn của mọi máy (mất liên lạc sau SCS_OFFLINE_AFTER, tự
// đóng cảnh báo high sau SCS_ALERT_AUTOCLOSE, đóng giai đoạn của máy bị thu hồi) mỗi phút.
func Worker(d Deps) func(context.Context) error {
	return func(ctx context.Context) error {
		if d.Registry == nil {
			d.Log.Warn("worker không có kho máy, không kiểm giai đoạn")
			<-ctx.Done()
			return nil
		}
		d.Log.Info("worker đang chạy: kiểm mất liên lạc và tự đóng cảnh báo mỗi phút")
		t := time.NewTicker(periodTickEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case now := <-t.C:
				n, err := d.Registry.PeriodTick(ctx, now)
				if err != nil && ctx.Err() == nil {
					d.Log.Error("kiểm giai đoạn thất bại", "err", err)
				} else if n > 0 {
					d.Log.Info("giai đoạn máy thay đổi (mất liên lạc / tự đóng cảnh báo)", "so_may", n)
				}
			}
		}
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

// Package heartbeat: máy đã duyệt gọi về server mỗi vài phút để báo còn sống và nhận những gì
// server gửi xuống: nhịp gọi, danh sách địa chỉ server, cấu hình agent, bản agent cần chạy.
// Trạng thái máy gửi kèm ở mốc A2.
package heartbeat

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"runtime"
	"slices"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/client"
	"github.com/thangkes/SCS-System/agent/internal/config"
	"github.com/thangkes/SCS-System/agent/internal/identity"
	"github.com/thangkes/SCS-System/agent/internal/sysinfo"
	"github.com/thangkes/SCS-System/agent/internal/update"
	"github.com/thangkes/SCS-System/common/protocol"
)

// Nhịp gọi (test đổi được).
var (
	Every      = 5 * time.Minute // khi server chưa trả next_poll_s
	MinEvery   = 1 * time.Minute // server trả nhịp nhỏ hơn thì vẫn chờ ít nhất bấy lâu
	MaxEvery   = 1 * time.Hour
	BackoffMin = 30 * time.Second
	BackoffMax = 30 * time.Minute
	// RetryUpdate: tải / kiểm bản agent mới lỗi thì bấy lâu sau mới thử lại (không thử mỗi heartbeat,
	// nhưng cũng không bỏ hẳn tới khi tiến trình khởi động lại).
	RetryUpdate = 1 * time.Hour
)

// ErrRestart: đã tải bản agent mới, cần khởi động lại để chạy bản đó (systemd tự bật lại).
var ErrRestart = errors.New("đã tải bản agent mới, khởi động lại để chạy")

// Deps là những gì vòng heartbeat cần.
type Deps struct {
	Log        *slog.Logger
	Client     *client.Client
	StateDir   string
	State      identity.State
	Version    string // phiên bản agent đang chạy
	ReleasePub string // khóa công khai phát hành (kiểm bản cập nhật)
}

// Run gửi heartbeat tới khi ctx bị hủy, hoặc trả ErrRestart khi đã tải xong bản agent mới.
// Server báo máy bị thu hồi (403) thì ghi log và giãn nhịp tối đa, không thoát (systemd không
// khởi động lại liên tục).
func Run(ctx context.Context, d Deps) error {
	st := d.State
	start := time.Now()
	backoff := BackoffMin
	lastServer := d.Client.Current()
	marked := false
	failedUpdate, failedAt := "", time.Time{} // bản tải / kiểm lỗi lần trước và lúc lỗi
	for {
		comps, _ := sysinfo.Components()
		body := protocol.HeartbeatBody{UptimeS: int64(time.Since(start) / time.Second), RuleSetVersion: st.RuleSetVersion,
			Arch: runtime.GOARCH, ConfigVersion: st.ConfigVersion,
			ServersHash: protocol.ServersHash(st.Servers), ServerURL: d.Client.Current()}
		var resp protocol.Response
		err := d.Client.Send(ctx, "/v1/heartbeat", protocol.TypeHeartbeat, st.DeviceID, comps.Hash(), body, &resp)

		var wait time.Duration
		var he *client.HTTPError
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.As(err, &he) && (he.Status == http.StatusForbidden || he.Status == http.StatusUnauthorized):
			d.Log.Error("server từ chối heartbeat (máy bị thu hồi hoặc chứng chỉ không còn hiệu lực)", "err", err)
			wait, backoff = BackoffMax, BackoffMin
		case err != nil:
			wait = backoff
			if errors.As(err, &he) && he.RetryAfter > wait {
				wait = he.RetryAfter
			}
			backoff = min(backoff*2, BackoffMax)
			d.Log.Warn("gửi heartbeat thất bại, sẽ thử lại", "err", err, "dia_chi", d.Client.Servers(),
				"thu_lai_sau", wait.Round(time.Second).String())
		default:
			backoff = BackoffMin
			wait = Every
			if resp.NextPollS > 0 {
				wait = min(max(time.Duration(resp.NextPollS)*time.Second, MinEvery), MaxEvery)
			}
			if !marked { // bản này chạy được: không bị bản khung coi là hỏng
				if err := update.MarkGood(d.StateDir, d.Version); err != nil {
					d.Log.Warn("không ghi được trạng thái bản cập nhật", "err", err)
				}
				update.StampOK(d.StateDir, d.Version) // nếu đây là bản khung: nó gọi được server
				marked = true
			}
			st2, changed := apply(d, st, resp)
			if cur := d.Client.Current(); cur != st2.LastServer {
				st2.LastServer, changed = cur, true
			}
			if changed {
				st = st2
				if err := identity.SaveState(d.StateDir, st); err != nil {
					d.Log.Error("không lưu được cấu hình server gửi xuống", "err", err)
				}
			}
			if u := resp.Update; u != nil && u.Version != d.Version &&
				(u.Version != failedUpdate || time.Since(failedAt) >= RetryUpdate) {
				d.Log.Info("server yêu cầu chạy bản agent khác, đang tải", "tu", d.Version, "sang", u.Version)
				if err := update.Apply(ctx, d.Client, *u, d.StateDir, d.ReleasePub); err != nil {
					failedUpdate, failedAt = u.Version, time.Now()
					d.Log.Error("không cập nhật được bản agent", "version", u.Version, "err", err)
				} else {
					d.Log.Info("đã tải và kiểm chữ ký bản agent mới, khởi động lại để chạy", "version", u.Version)
					return ErrRestart
				}
			}
		}
		if cur := d.Client.Current(); cur != lastServer {
			d.Log.Warn("đã chuyển sang địa chỉ server khác", "tu", lastServer, "sang", cur)
			lastServer = cur
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter(wait)):
		}
	}
}

// apply: lưu danh sách địa chỉ và cấu hình server gửi xuống; trả state mới và có thay đổi không.
func apply(d Deps, st identity.State, resp protocol.Response) (identity.State, bool) {
	changed := false
	if list := protocol.CleanServers(resp.Servers); len(list) > 0 && !slices.Equal(list, st.Servers) {
		st.Servers = list
		d.Client.SetServers(list)
		d.Log.Info("đã nhận danh sách địa chỉ server", "dia_chi", d.Client.Servers())
		changed = true
	}
	if c := resp.Config; c != nil && c.Version != st.ConfigVersion {
		watch := make([]string, 0, len(c.WatchServices))
		for _, s := range c.WatchServices {
			if config.ValidServiceName(s) && len(watch) < protocol.MaxServices {
				watch = append(watch, s)
			}
		}
		st.ConfigVersion, st.WatchServices = c.Version, watch
		d.Log.Info("đã nhận cấu hình agent từ server", "dich_vu_theo_doi", watch)
		changed = true
	}
	return st, changed
}

// jitter: ±20% để các máy không gọi về cùng một lúc.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d + time.Duration((rand.Float64()*0.4-0.2)*float64(d))
}

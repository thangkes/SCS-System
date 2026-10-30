// Package heartbeat: máy đã duyệt gọi về server mỗi vài phút để báo còn sống và nhận cấu hình
// server gửi xuống (nhịp gọi, danh sách địa chỉ server). Trạng thái máy gửi kèm ở mốc A2.
package heartbeat

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"slices"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/client"
	"github.com/thangkes/SCS-System/agent/internal/identity"
	"github.com/thangkes/SCS-System/agent/internal/sysinfo"
	"github.com/thangkes/SCS-System/common/protocol"
)

// Nhịp gọi (test đổi được).
var (
	Every      = 5 * time.Minute // khi server chưa trả next_poll_s
	MinEvery   = 1 * time.Minute // server trả nhịp nhỏ hơn thì vẫn chờ ít nhất bấy lâu
	MaxEvery   = 1 * time.Hour
	BackoffMin = 30 * time.Second
	BackoffMax = 30 * time.Minute
)

// Deps là những gì vòng heartbeat cần.
type Deps struct {
	Log      *slog.Logger
	Client   *client.Client
	StateDir string
	State    identity.State
}

// Run gửi heartbeat tới khi ctx bị hủy. Server báo máy bị thu hồi (403) thì ghi log và
// giãn nhịp tối đa, không thoát (systemd không khởi động lại liên tục).
func Run(ctx context.Context, d Deps) error {
	st := d.State
	start := time.Now()
	backoff := BackoffMin
	lastServer := d.Client.Current()
	for {
		comps, _ := sysinfo.Components()
		body := protocol.HeartbeatBody{UptimeS: int64(time.Since(start) / time.Second), RuleSetVersion: st.RuleSetVersion}
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
			if list := protocol.CleanServers(resp.Servers); len(list) > 0 && !slices.Equal(list, st.Servers) {
				st.Servers = list
				d.Client.SetServers(list)
				if err := identity.SaveState(d.StateDir, st); err != nil {
					d.Log.Error("không lưu được danh sách server", "err", err)
				} else {
					d.Log.Info("đã nhận danh sách địa chỉ server", "dia_chi", d.Client.Servers())
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

// jitter: ±20% để các máy không gọi về cùng một lúc.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d + time.Duration((rand.Float64()*0.4-0.2)*float64(d))
}

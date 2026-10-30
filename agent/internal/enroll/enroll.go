// Package enroll: vòng đăng ký của agent. Gửi gói enroll (ký bằng khóa máy), chờ admin duyệt
// (hỏi lại mỗi 5 phút bằng cùng khóa), được duyệt thì kiểm và lưu chứng chỉ server cấp.
package enroll

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/client"
	"github.com/thangkes/SCS-System/agent/internal/identity"
	"github.com/thangkes/SCS-System/agent/internal/sysinfo"
	"github.com/thangkes/SCS-System/common/protocol"
)

// Nhịp gọi (test đổi được).
var (
	PendingEvery = 5 * time.Minute  // đang chờ duyệt: hỏi lại sau bấy lâu (±20%)
	BackoffMin   = 30 * time.Second // lỗi mạng / server bận: chờ ít nhất bấy lâu, gấp đôi mỗi lần
	BackoffMax   = 30 * time.Minute
)

// Deps là những gì vòng đăng ký cần.
type Deps struct {
	Log      *slog.Logger
	Client   *client.Client
	Key      *identity.Key
	KeyDir   string
	StateDir string
	CAFile   string // CA của server: chứng chỉ nhận về phải do CA này ký
}

// Run chạy tới khi máy được duyệt (trả trạng thái approved) hoặc bị từ chối (rejected),
// hoặc ctx bị hủy.
func Run(ctx context.Context, d Deps) (identity.State, error) {
	st, err := identity.LoadState(d.StateDir)
	if err != nil {
		return st, err
	}
	backoff := BackoffMin
	warned := false
	for {
		comps, problems := sysinfo.Components()
		if len(problems) > 0 && !warned {
			d.Log.Warn("thiếu thành phần nhận dạng máy (vẫn đăng ký được)", "chi_tiet", problems)
			warned = true
		}
		body := protocol.EnrollBody{
			PublicKey: protocol.EncodePublicKey(d.Key.Public), Components: comps,
			Hostname: sysinfo.Hostname(), OSVersion: sysinfo.OSVersion(),
		}
		var resp protocol.EnrollResponse
		err := d.Client.Send(ctx, "/v1/enroll", protocol.TypeEnroll, "", comps.Hash(), body, &resp)

		var wait time.Duration
		switch {
		case ctx.Err() != nil:
			return st, ctx.Err()
		case err != nil:
			var he *client.HTTPError
			wait = backoff
			if errors.As(err, &he) && he.RetryAfter > wait {
				wait = he.RetryAfter
			}
			backoff = min(backoff*2, BackoffMax)
			d.Log.Warn("gửi đăng ký thất bại, sẽ thử lại", "err", err, "thu_lai_sau", wait.Round(time.Second).String(),
				"lech_gio", d.Client.Offset().Round(time.Second).String())
		default:
			backoff = BackoffMin
			switch resp.Status {
			case protocol.EnrollPending:
				if st.Status != identity.StatusPending {
					st = identity.State{Status: identity.StatusPending, RequestedAt: time.Now().UTC()}
					if err := identity.SaveState(d.StateDir, st); err != nil {
						return st, err
					}
					d.Log.Info("đã gửi đăng ký, chờ admin duyệt trên web", "hostname", body.Hostname, "ma_khoa", d.Key.ID())
				}
				wait = PendingEvery
			case protocol.EnrollApproved:
				if err := checkCert(resp, d); err != nil {
					d.Log.Error("chứng chỉ server cấp không hợp lệ, không lưu", "err", err)
					wait = BackoffMax
					break
				}
				if err := identity.SaveCert(d.KeyDir, []byte(resp.CertificatePEM)); err != nil {
					return st, err
				}
				d.Client.ResetSessions() // không nối lại phiên TLS lập lúc chưa có chứng chỉ
				st.Status, st.DeviceID, st.ApprovedAt = identity.StatusApproved, resp.DeviceID, time.Now().UTC()
				if st.RequestedAt.IsZero() { // được duyệt ngay lần gửi đầu
					st.RequestedAt = st.ApprovedAt
				}
				st.RuleSetVersion = resp.RuleSetVersion
				if list := protocol.CleanServers(resp.Servers); len(list) > 0 {
					st.Servers = list
					d.Client.SetServers(list)
				}
				if err := identity.SaveState(d.StateDir, st); err != nil {
					return st, err
				}
				d.Log.Info("máy đã được duyệt, đã nhận chứng chỉ", "device_id", resp.DeviceID)
				return st, nil
			case protocol.EnrollRejected:
				st.Status = identity.StatusRejected
				if err := identity.SaveState(d.StateDir, st); err != nil {
					return st, err
				}
				d.Log.Error("admin đã từ chối hoặc thu hồi máy này; agent dừng gửi dữ liệu")
				return st, nil
			default:
				d.Log.Warn("server trả trạng thái đăng ký lạ", "status", resp.Status)
				wait = PendingEvery
			}
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(jitter(wait)):
		}
	}
}

// checkCert: chứng chỉ phải do CA của server ký, mang đúng khóa của máy này, đúng mã máy,
// dùng để xác thực client và còn hạn.
func checkCert(resp protocol.EnrollResponse, d Deps) error {
	blk, _ := pem.Decode([]byte(resp.CertificatePEM))
	if blk == nil || blk.Type != "CERTIFICATE" {
		return errors.New("không phải chứng chỉ PEM")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return err
	}
	caPEM, err := os.ReadFile(d.CAFile)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("không do CA của server ký: %w", err)
	}
	if !d.Key.Public.Equal(cert.PublicKey) {
		return errors.New("chứng chỉ không mang khóa của máy này")
	}
	if resp.DeviceID == "" || cert.Subject.CommonName != resp.DeviceID {
		return errors.New("mã máy trong chứng chỉ không khớp")
	}
	return nil
}

// jitter: ±20% để các máy không gọi về cùng một lúc.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d + time.Duration((rand.Float64()*0.4-0.2)*float64(d))
}

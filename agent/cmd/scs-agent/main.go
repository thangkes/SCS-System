// scs-agent: chạy trên từng máy siêu thị, gửi trạng thái và sự kiện log về server.
// Agent chỉ chủ động gọi lên server; server không gửi lệnh xuống.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/client"
	"github.com/thangkes/SCS-System/agent/internal/config"
	"github.com/thangkes/SCS-System/agent/internal/enroll"
	"github.com/thangkes/SCS-System/agent/internal/heartbeat"
	"github.com/thangkes/SCS-System/agent/internal/identity"
	"github.com/thangkes/SCS-System/agent/internal/smart"
	"github.com/thangkes/SCS-System/agent/internal/sysinfo"
	"github.com/thangkes/SCS-System/agent/internal/update"
	"github.com/thangkes/SCS-System/agent/internal/watch"
	"github.com/thangkes/SCS-System/common/fleet"
	"github.com/thangkes/SCS-System/common/protocol"
)

// version được gán lúc build: -ldflags "-X main.version=1.0.0"
var version = "dev"

const usage = `scs-agent - SCS-Agent, agent giám sát máy siêu thị của SCS-System

Cách dùng:
  scs-agent <lệnh> [-config /etc/scs-agent/agent.env]

Lệnh:
  run          chạy agent (dịch vụ systemd gọi lệnh này)
  fingerprint  xem hostname có đúng quy ước không, các thành phần nhận dạng máy, mã khóa
  status       xem cấu hình, trạng thái đăng ký và trạng thái máy sẽ gửi trong heartbeat
  ping         kiểm tra kết nối và chứng chỉ server (-server URL -ca FILE: thử trước khi cài)
  rules        xem bộ rule đang dùng, cài tay, chạy thử trên file log (scs-agent rules -h)
  promote      (root, do scs-agent-promote.timer gọi) thay bản khung bằng bản mới đã chạy tốt, có kiểm chữ ký
  smart        (root, do scs-agent-smart.timer gọi) đọc SMART các ổ, ghi kết quả cho agent gửi lên (-out FILE)
  renew        xin server cấp lại chứng chỉ máy ngay (chạy bằng user scs-agent: sudo -u scs-agent scs-agent renew)
  version      in phiên bản

Hostname phải đúng quy ước <mã siêu thị>-TN<số>[-PRI|-SEC]:
  12345-TN1-PRI (máy 1), 12345-TN2-SEC (máy 2), 12345-TN3, 12345-TN4...
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "rules":
		return runRules(args[1:], stdout, stderr)
	case "run", "fingerprint", "status", "ping", "promote", "smart", "renew":
	default:
		fmt.Fprintf(stderr, "lệnh không hợp lệ: %q\n\n%s", cmd, usage)
		return 2
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "file cấu hình (mặc định "+config.DefaultFile+")")
	server := fs.String("server", "", "ping: địa chỉ server cần thử (mặc định lấy từ cấu hình)")
	caFile := fs.String("ca", "", "ping: CA của server (mặc định lấy từ cấu hình)")
	installDir := fs.String("install-dir", "/opt/scs-agent", "promote: thư mục cài bản khung")
	smartOut := fs.String("out", smart.DefaultFile, "smart: file kết quả (thư mục phải thuộc root)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if cmd == "ping" && *cfgPath == "" && *server != "" && *caFile != "" {
		*cfgPath = os.DevNull // thử trước khi cài: không cần file cấu hình
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "cấu hình không hợp lệ:\n"+err.Error())
		return 1
	}
	if *server != "" {
		cfg.Server = *server
	}
	if *caFile != "" {
		cfg.ServerCA = *caFile
	}

	if cmd == "smart" {
		return runSmart(stdout, stderr, *smartOut)
	}
	switch cmd {
	case "fingerprint":
		printFingerprint(stdout, cfg)
		return 0
	case "status":
		return printStatus(stdout, stderr, cfg)
	case "ping":
		return ping(stdout, stderr, cfg)
	case "promote":
		return promote(stdout, stderr, cfg, *installDir)
	case "renew":
		return renewNow(stdout, stderr, cfg)
	}
	return runAgent(stdout, stderr, cfg)
}

// promote: một lượt thay bản khung (xem internal/update/promote.go). Phải chạy bằng root.
// renewNow: xin cấp lại chứng chỉ máy ngay (khi máy bị báo chứng chỉ sắp / đã hết hạn mà chưa tự gia hạn). Dùng
// chính khóa máy; server chỉ cấp cho máy đã duyệt, đúng khóa, đúng mã máy. Chạy bằng user scs-agent (chủ khóa).
func renewNow(stdout, stderr io.Writer, cfg *config.Config) int {
	if err := cfg.RequireServer(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	key, err := identity.LoadKey(cfg.KeyDir)
	if err != nil {
		fmt.Fprintln(stderr, "không đọc được khóa máy (chạy bằng: sudo -u scs-agent scs-agent renew):", err)
		return 1
	}
	st, err := identity.LoadState(cfg.StateDir)
	if err != nil || st.Status != identity.StatusApproved || st.DeviceID == "" {
		fmt.Fprintln(stderr, "máy chưa được duyệt: không có chứng chỉ để gia hạn")
		return 1
	}
	if na, err := identity.CertNotAfter(cfg.KeyDir); err == nil {
		fmt.Fprintf(stdout, "Chứng chỉ hiện tại hết hạn: %s\n", na.Local().Format("15:04 02/01/2006"))
	}
	c, err := client.New(cfg.Server, cfg.ServerCA, cfg.KeyDir, key, version)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	c.SetServers(st.Servers)
	c.Prefer(st.LastServer)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ok, err := enroll.Renew(ctx, enroll.Deps{Log: newLogger(io.Discard, "error"), Client: c, Key: key, KeyDir: cfg.KeyDir,
		StateDir: cfg.StateDir, CAFile: cfg.ServerCA}, st.DeviceID)
	switch {
	case err != nil:
		fmt.Fprintln(stderr, "✖ không gia hạn được:", err)
		return 1
	case !ok:
		fmt.Fprintln(stdout, "Server chưa cấp (đang hết lượt tải): thử lại sau ít phút")
		return 1
	}
	na, _ := identity.CertNotAfter(cfg.KeyDir)
	fmt.Fprintf(stdout, "✔ Đã nhận chứng chỉ mới, hết hạn %s. Agent dùng ở lần gọi tới (hoặc: sudo systemctl restart scs-agent)\n",
		na.Local().Format("15:04 02/01/2006"))
	return 0
}

// runSmart: tác vụ root riêng (scs-agent-smart.service, chỉ CAP_SYS_RAWIO + CAP_SYS_ADMIN, thiết bị ổ chỉ đọc, không
// mạng) đọc SMART rồi ghi file kết quả; agent thường chỉ đọc file đó. Không cần file cấu hình.
func runSmart(stdout, stderr io.Writer, out string) int {
	if os.Geteuid() != 0 && os.Geteuid() != -1 {
		fmt.Fprintln(stderr, "smart phải chạy bằng root (dịch vụ scs-agent-smart gọi lệnh này)")
		return 1
	}
	rep := smart.Read(time.Now())
	if err := smart.Save(out, rep); err != nil {
		fmt.Fprintln(stderr, "✖ không ghi được kết quả SMART:", err)
		return 1
	}
	for _, d := range rep.Disks {
		switch {
		case !d.OK:
			fmt.Fprintf(stdout, "%-10s %-4s không đọc được: %s\n", d.Name, d.Kind, d.Note)
		case d.Passed != nil && !*d.Passed:
			fmt.Fprintf(stdout, "%-10s %-4s Ổ BÁO LỖI: %s\n", d.Name, d.Kind, strings.Join(d.Failing, "; "))
		default:
			fmt.Fprintf(stdout, "%-10s %-4s đọc được\n", d.Name, d.Kind)
		}
	}
	if rep.Error != "" {
		fmt.Fprintln(stderr, rep.Error)
	}
	fmt.Fprintf(stdout, "Đã ghi %s (%d ổ)\n", out, len(rep.Disks))
	return 0
}

func promote(stdout, stderr io.Writer, cfg *config.Config, installDir string) int {
	if os.Geteuid() != 0 && os.Geteuid() != -1 {
		fmt.Fprintln(stderr, "promote phải chạy bằng root (dịch vụ scs-agent-promote gọi lệnh này)")
		return 1
	}
	msg, err := update.Promote(cfg.StateDir, installDir, cfg.ReleasePub)
	if err != nil {
		fmt.Fprintln(stderr, "✖", err)
		return 1
	}
	if msg == "" {
		msg = "không có gì để làm"
	}
	fmt.Fprintln(stdout, msg)
	return 0
}

// runAgent: vòng chạy chính: đăng ký và chờ duyệt (A1), rồi heartbeat; gửi log ở A3.
// Bị từ chối hoặc lỗi không tự khỏi thì không thoát (để systemd không khởi động lại liên tục),
// chỉ ghi log và chờ tới khi bị dừng.
func runAgent(stdout, stderr io.Writer, cfg *config.Config) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := newLogger(stdout, cfg.LogLevel).With("version", version)
	if err := cfg.RequireServer(); err != nil {
		log.Error("không khởi động được", "err", err)
		return 1
	}
	// Bản khung ghi dấu vừa khởi động (tác vụ thay bản khung dùng để biết bản khung mới có chạy được không).
	update.StampStart(cfg.StateDir, version)
	// Bản khung: có bản cập nhật đã kiểm chữ ký thì chuyển sang chạy bản đó (không quay lại đây).
	update.Launch(log, cfg.StateDir, cfg.ReleasePub, version)
	if _, err := fleet.ParseHostname(sysinfo.Hostname()); err != nil {
		// Vẫn chạy: server ghi nhận và gắn cảnh báo, admin quyết định khi duyệt.
		log.Warn("hostname sai quy ước, admin sẽ thấy cảnh báo khi duyệt máy", "err", err)
	}
	key, err := identity.LoadOrCreateKey(cfg.KeyDir)
	if err != nil {
		log.Error("không khởi động được", "err", err)
		return 1
	}
	st, err := identity.LoadState(cfg.StateDir)
	if err != nil {
		log.Error("không khởi động được", "err", err)
		return 1
	}
	log.Info("agent khởi động", "server", cfg.Server, "ma_khoa", key.ID(), "trang_thai", st.Status)

	c, err := client.New(cfg.Server, cfg.ServerCA, cfg.KeyDir, key, version)
	if err != nil {
		log.Error("không khởi động được", "err", err)
		return 1
	}
	c.SetServers(st.Servers) // địa chỉ server gửi xuống từ lần trước
	c.Prefer(st.LastServer)
	_, certErr := os.Stat(identity.CertPath(cfg.KeyDir))
	enrolledBefore := st.Status == identity.StatusApproved && certErr == nil // không phải lần chạy đầu / đang đăng ký
	if st.Status != identity.StatusApproved || certErr != nil {
		st, err = enroll.Run(ctx, enroll.Deps{Log: log, Client: c, Key: key,
			KeyDir: cfg.KeyDir, StateDir: cfg.StateDir, CAFile: cfg.ServerCA})
		if ctx.Err() != nil {
			log.Info("đã dừng")
			return 0
		}
		if err != nil {
			log.Error("đăng ký thất bại", "err", err)
			return 1
		}
	}
	switch st.Status {
	case identity.StatusApproved:
		log.Info("đã được duyệt, bắt đầu gửi heartbeat", "device_id", st.DeviceID, "dia_chi", c.Servers())
		// Quét log liên tục theo bộ rule đang cài, gửi sự kiện lên server (mốc A3).
		sc := &watch.Scanner{Log: log.With("phan", "quet-log"), StateDir: cfg.StateDir, ReleasePub: cfg.ReleasePub,
			Client: c, DeviceID: st.DeviceID, Fingerprint: func() string { comps, _ := sysinfo.Components(); return comps.Hash() }}
		scanCtx, stopScan := context.WithCancel(ctx)
		scanDone := make(chan struct{})
		go func() { defer close(scanDone); sc.Run(scanCtx) }()
		deviceID := st.DeviceID
		renew := func(ctx context.Context) (bool, error) {
			return enroll.Renew(ctx, enroll.Deps{Log: log, Client: c, Key: key, KeyDir: cfg.KeyDir,
				StateDir: cfg.StateDir, CAFile: cfg.ServerCA}, deviceID)
		}
		// Máy vừa khởi động (lịch reboot cùng giờ mỗi sáng) và đã duyệt từ trước: dời heartbeat đầu 5–30 phút.
		var firstDelay time.Duration
		if up, err := sysinfo.Uptime(); err == nil && enrolledBefore {
			last, ok := heartbeat.LastHeartbeat(cfg.StateDir)
			firstDelay = heartbeat.BootDelay(time.Duration(up)*time.Second, time.Since(last), ok)
		}
		err := heartbeat.Run(ctx, heartbeat.Deps{Log: log, Client: c, StateDir: cfg.StateDir, State: st,
			Version: version, ReleasePub: cfg.ReleasePub, Scanner: sc, KeyDir: cfg.KeyDir, Renew: renew,
			FirstDelay: firstDelay, Services: cfg.WatchServices, Status: func(svcs []string) *protocol.MachineStatus {
				st := sysinfo.Status(svcs)
				st.Smart = smart.Load(smart.DefaultFile) // tác vụ root ghi, agent chỉ đọc
				return st
			}})
		stopScan()
		<-scanDone
		if errors.Is(err, heartbeat.ErrRestart) {
			return 0 // systemd bật lại (Restart=always), bản khung chuyển sang bản vừa tải
		}
	case identity.StatusRejected:
		log.Error("máy bị từ chối hoặc thu hồi; muốn dùng lại: xóa khóa máy, cài lại agent và chờ duyệt lại")
	}
	<-ctx.Done()
	log.Info("đã dừng")
	return 0
}

// ping: gọi /healthz qua mọi địa chỉ đã biết (địa chỉ lúc cài + danh sách server gửi xuống).
func ping(stdout, stderr io.Writer, cfg *config.Config) int {
	if err := cfg.RequireServer(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	key, err := identity.LoadKey(cfg.KeyDir)
	if err != nil { // chưa cài: khóa tạm, /healthz không cần chứng chỉ máy
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		key = &identity.Key{Public: pub, Private: priv}
	}
	c, err := client.New(cfg.Server, cfg.ServerCA, cfg.KeyDir, key, version)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if st, err := identity.LoadState(cfg.StateDir); err == nil {
		c.SetServers(st.Servers)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := c.Ping(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "không kết nối được server:\n"+err.Error())
		return 1
	}
	fmt.Fprintf(stdout, "OK: %s trả lời, chứng chỉ server do CA %s cấp\n", base, cfg.ServerCA)
	return 0
}

func printFingerprint(w io.Writer, cfg *config.Config) {
	host := sysinfo.Hostname()
	if n, err := fleet.ParseHostname(host); err == nil {
		role := string(n.Role)
		if role == "" {
			role = "máy phụ"
		}
		fmt.Fprintf(w, "Hostname         : %s  [đúng quy ước: siêu thị %s, máy TN%d, %s]\n", host, n.Store, n.TN, role)
	} else {
		fmt.Fprintf(w, "Hostname         : %s  [SAI QUY ƯỚC]\n                   %v\n", host, err)
	}
	c, errs := sysinfo.Components()
	fmt.Fprintf(w, "machine-id       : %s\n", orDash(c.MachineID))
	fmt.Fprintf(w, "Serial mainboard : %s\n", orDash(c.BoardSerial))
	fmt.Fprintf(w, "Serial hệ thống  : %s\n", orDash(c.SystemSerial))
	fmt.Fprintf(w, "MAC chính        : %s\n", orDash(c.PrimaryMAC))
	fmt.Fprintf(w, "Fingerprint      : %s\n", c.Hash())
	switch k, err := identity.LoadKey(cfg.KeyDir); {
	case err == nil:
		fmt.Fprintf(w, "Mã khóa máy      : %s  (đối chiếu với yêu cầu chờ duyệt trên web)\n", k.ID())
	case errors.Is(err, identity.ErrNoKey):
		fmt.Fprintln(w, "Mã khóa máy      : chưa có (tạo khi agent chạy lần đầu)")
	default:
		fmt.Fprintf(w, "Mã khóa máy      : LỖI: %v\n", err)
	}
	for _, e := range errs {
		fmt.Fprintln(w, "Lưu ý            : "+e)
	}
}

func printStatus(stdout, stderr io.Writer, cfg *config.Config) int {
	st, err := identity.LoadState(cfg.StateDir)
	if err != nil {
		fmt.Fprintln(stderr, "đọc trạng thái:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Server            : %s\n", orDash(cfg.Server))
	if len(st.Servers) > 0 {
		fmt.Fprintf(stdout, "Địa chỉ dự phòng  : %s (server gửi xuống)\n", strings.Join(st.Servers, ", "))
	}
	watch, from := cfg.WatchServices, "agent.env"
	if st.WatchServices != nil {
		watch, from = st.WatchServices, "server gửi xuống"
	}
	fmt.Fprintf(stdout, "Dịch vụ theo dõi  : %s (%s)\n", orDash(strings.Join(watch, ", ")), from)
	fmt.Fprintf(stdout, "Phiên bản agent   : %s\n", version)
	if cur, err := update.Load(cfg.StateDir); err == nil && cur.Version != "" {
		state := "chờ khởi động lại"
		if cur.Good {
			state = "đang chạy tốt"
		}
		fmt.Fprintf(stdout, "Bản cập nhật      : %s (%s)\n", cur.Version, state)
	}
	fmt.Fprintf(stdout, "Trạng thái đăng ký: %s\n", st.Status)
	if st.DeviceID != "" {
		fmt.Fprintf(stdout, "Mã máy (device_id): %s\n", st.DeviceID)
	}
	if na, err := identity.CertNotAfter(cfg.KeyDir); err == nil {
		left := time.Until(na)
		note := fmt.Sprintf("còn %d ngày; agent tự gia hạn khi còn dưới 30 ngày", int(left.Hours()/24))
		if left <= 0 {
			note = "ĐÃ HẾT HẠN — chạy: sudo -u scs-agent scs-agent renew"
		}
		fmt.Fprintf(stdout, "Chứng chỉ máy     : hết hạn %s (%s)\n", na.Local().Format("15:04 02/01/2006"), note)
	}
	b, _ := json.MarshalIndent(sysinfo.Status(watch), "", "  ")
	fmt.Fprintf(stdout, "\nTrạng thái máy sẽ gửi trong heartbeat:\n%s\n", b)
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func newLogger(w io.Writer, level string) *slog.Logger {
	var lv slog.Level
	lv.UnmarshalText([]byte(level)) // config.Load đã kiểm giá trị hợp lệ
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv}))
}

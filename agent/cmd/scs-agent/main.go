// scs-agent: chạy trên từng máy siêu thị, gửi trạng thái và sự kiện log về server.
// Agent chỉ chủ động gọi lên server; server không gửi lệnh xuống.
package main

import (
	"context"
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

	"github.com/thangkes/SCS-System/agent/internal/client"
	"github.com/thangkes/SCS-System/agent/internal/config"
	"github.com/thangkes/SCS-System/agent/internal/enroll"
	"github.com/thangkes/SCS-System/agent/internal/identity"
	"github.com/thangkes/SCS-System/agent/internal/sysinfo"
	"github.com/thangkes/SCS-System/common/fleet"
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
	case "run", "fingerprint", "status":
	default:
		fmt.Fprintf(stderr, "lệnh không hợp lệ: %q\n\n%s", cmd, usage)
		return 2
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "file cấu hình (mặc định "+config.DefaultFile+")")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "cấu hình không hợp lệ:\n"+err.Error())
		return 1
	}

	switch cmd {
	case "fingerprint":
		printFingerprint(stdout, cfg)
		return 0
	case "status":
		return printStatus(stdout, stderr, cfg)
	}
	return runAgent(stdout, stderr, cfg)
}

// runAgent: vòng chạy chính: đăng ký và chờ duyệt (A1); heartbeat, gửi log ở A2–A3.
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
	_, certErr := os.Stat(identity.CertPath(cfg.KeyDir))
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
		log.Info("đã được duyệt; heartbeat và gửi log sẽ có ở mốc A2–A3", "device_id", st.DeviceID)
	case identity.StatusRejected:
		log.Error("máy bị từ chối hoặc thu hồi; muốn dùng lại: xóa khóa máy, cài lại agent và chờ duyệt lại")
	}
	<-ctx.Done()
	log.Info("đã dừng")
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
	fmt.Fprintf(stdout, "Dịch vụ theo dõi  : %s\n", orDash(strings.Join(cfg.WatchServices, ", ")))
	fmt.Fprintf(stdout, "Trạng thái đăng ký: %s\n", st.Status)
	if st.DeviceID != "" {
		fmt.Fprintf(stdout, "Mã máy (device_id): %s\n", st.DeviceID)
	}
	b, _ := json.MarshalIndent(sysinfo.Status(cfg.WatchServices), "", "  ")
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

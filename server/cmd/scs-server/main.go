// scs-server: một binary, nhiều lệnh con (ingest, admin, worker, all).
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thangkes/SCS-System/server/internal/addrbook"
	"github.com/thangkes/SCS-System/server/internal/app"
	"github.com/thangkes/SCS-System/server/internal/auth"
	"github.com/thangkes/SCS-System/server/internal/cluster"
	"github.com/thangkes/SCS-System/server/internal/config"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/periods"
	"github.com/thangkes/SCS-System/server/internal/pki"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
	"github.com/thangkes/SCS-System/server/internal/store"
	"github.com/thangkes/SCS-System/server/internal/sysstat"
)

// version được gán lúc build: -ldflags "-X main.version=1.0.0"
var version = "dev"

const usage = `scs-server - SCS-Server, trung tâm của SCS-System

Cách dùng:
  scs-server <lệnh> [-env-file đường_dẫn]

Lệnh:
  ingest    API cho agent: đăng ký, heartbeat, log, rule, cập nhật (cổng 6222)
  admin       web quản trị HTTPS (cổng 7222), đăng nhập bằng mã 6 số (SSH hoặc gửi qua email)
  worker      xử lý hàng đợi
  all         chạy tất cả trong một tiến trình (dùng khi dev)
  login-code  xem mã đăng nhập web quản trị (chạy trên máy server; -watch để tự cập nhật,
              -unlock để mở khóa khi bị khóa do nhập sai nhiều)
  pki         tạo CA nội bộ + chứng chỉ server ghi IP WAN: pki init -ip <IP WAN> [-ip <IP VPN>]
              cấp lại chứng chỉ server (đổi IP): pki server -ip ...
  rules       viết rule quét log: rules check | test | build (scs-server rules -h)
  cluster     cụm nhiều server: cluster init | status | ping (scs-server cluster -h)
  servers     địa chỉ server gửi cho agent: servers list | add | remove | set (scs-server servers -h)
  mail-test   gửi thư thử bằng cấu hình SCS_SMTP_* (Gmail gửi mã đăng nhập): mail-test -to <email>
  version     in phiên bản

Cấu hình lấy từ biến môi trường; tự nạp ./.env nếu có (xem .env.example).
`

type runner func(context.Context) error

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
	case "pki":
		return runPKI(args[1:], stdout, stderr)
	case "rules":
		return runRules(args[1:], stdout, stderr)
	case "cluster":
		return runCluster(args[1:], stdout, stderr)
	case "servers":
		return runServers(args[1:], stdout, stderr)
	case "mail-test":
		return runMailTest(args[1:], stdout, stderr)
	case "ingest", "admin", "worker", "all", "login-code":
	default:
		fmt.Fprintf(stderr, "lệnh không hợp lệ: %q\n\n%s", cmd, usage)
		return 2
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envFile := fs.String("env-file", "", "file .env cần nạp (mặc định: ./.env nếu có)")
	watch := fs.Bool("watch", false, "login-code: tự in mã mới mỗi khi mã đổi, Ctrl+C để thoát")
	unlock := fs.Bool("unlock", false, "login-code: mở khóa đăng nhập web (bị khóa do nhập sai nhiều)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	loaded, warnings, err := loadEnv(*envFile)
	if err != nil {
		fmt.Fprintln(stderr, "lỗi đọc .env:", err)
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "cấu hình không hợp lệ:\n"+err.Error())
		return 1
	}

	if cmd == "login-code" {
		if *unlock {
			if err := auth.UnlockLogin(cfg.ControlSocket); err != nil {
				fmt.Fprintf(stderr, "Không mở khóa được qua %s: %v\n", cfg.ControlSocket, err)
				return 1
			}
			fmt.Fprintln(stdout, "Đã mở khóa đăng nhập web quản trị và xóa các lần nhập sai đã đếm.")
		}
		return loginCode(cfg.ControlSocket, *watch, stdout, stderr)
	}

	log := newLogger(stdout, cfg.LogLevel).With("version", version)
	if loaded != "" {
		log.Info("đã nạp cấu hình", "env_file", loaded, "env", cfg.Env)
	}
	for _, w := range warnings {
		log.Warn(w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL())
	if err != nil {
		log.Error("không khởi động được", "err", err)
		return 1
	}
	defer db.Close()
	log.Info("PostgreSQL", "host", cfg.DB.Host, "port", cfg.DB.Port, "db", cfg.DB.Name, "user", cfg.DB.User)

	// Cập nhật cấu trúc DB chạy ngầm: DB chưa lên thì thử lại, /readyz báo chưa sẵn sàng tới khi xong.
	migrator, err := store.NewMigrator(db, log.With("role", "db"))
	if err != nil {
		log.Error("không khởi động được", "err", err)
		return 1
	}
	if cfg.OfflineAfter < 20*time.Minute {
		// Agent gọi về ngẫu nhiên 5–10 phút: hạn ngắn hơn 2 nhịp tối đa sẽ báo mất liên lạc oan.
		log.Warn("SCS_OFFLINE_AFTER ngắn: agent gọi về ngẫu nhiên 5–10 phút một lần, nên đặt từ 20m (mặc định 30m)",
			"SCS_OFFLINE_AFTER", cfg.OfflineAfter.String())
	}
	reg := store.NewRegistry(db)
	reg.Policy = periods.Policy{AutoClose: cfg.AlertAutoClose, OfflineAfter: cfg.OfflineAfter}
	base := app.Deps{
		ReadyChecks: []httpx.Check{store.ReadyCheck(db), migrator.Check()},
		Registry:    reg,
	}
	if cfg.CACert != "" {
		if base.CA, err = pki.Load(cfg.CACert, cfg.CAKey); err != nil {
			log.Error("không khởi động được", "err", err)
			return 1
		}
		log.Info("CA cấp chứng chỉ agent", "file", cfg.CACert, "het_han", base.CA.Cert.NotAfter.Format(time.DateOnly))
	} else if cmd == "ingest" || cmd == "all" {
		log.Warn("chưa khai SCS_CA_CERT/SCS_CA_KEY: máy đã duyệt sẽ không nhận được chứng chỉ (tạo bằng: scs-server pki init -ip <IP WAN>)")
	}
	if cfg.ReleaseKey != "" {
		if base.ReleaseKey, err = pki.LoadReleaseKey(cfg.ReleaseKey); err != nil {
			log.Error("không khởi động được", "err", err)
			return 1
		}
	} else if cmd == "ingest" || cmd == "all" {
		log.Warn("chưa khai SCS_RELEASE_KEY: server không phát hành bản agent (tạo bằng: scs-server pki release)")
	}
	if cmd == "ingest" || cmd == "all" || cmd == "admin" {
		// Một Book chung cho cổng agent và web: web thấy được số máy đã nhận danh sách địa chỉ.
		// Chạy ingest và admin thành hai tiến trình thì web chỉ sửa được danh sách, không có số liệu.
		base.Servers, err = addrbook.Open(cfg.AgentServersFile, cfg.AgentServers)
		if err != nil {
			log.Error("file danh sách địa chỉ gửi agent bị lỗi, tạm dùng SCS_AGENT_SERVERS (sửa bằng: scs-server servers set)",
				"err", err)
		}
		log.Info("địa chỉ gửi cho agent", "file", cfg.AgentServersFile, "dia_chi", strings.Join(base.Servers.URLs(), ", "))
	}
	if cmd == "admin" || cmd == "all" { // kho bộ rule cho trang web "Rule quét log"
		var pub ed25519.PublicKey
		if base.ReleaseKey != nil {
			pub = base.ReleaseKey.Public().(ed25519.PublicKey)
		}
		if base.Rules, err = rulestore.Open(cfg.RulesDir, pub); err != nil {
			log.Error("không mở được kho bộ rule: trang Rule quét log sẽ báo lỗi", "dir", cfg.RulesDir, "err", err)
		}
	}
	if cmd == "all" { // phần cụm chạy cùng tiến trình với web quản trị (trang "Cụm server" dùng chung node)
		if base.Cluster, err = cluster.Open(cfg.ClusterDir); err != nil {
			log.Error("không khởi động được", "err", fmt.Errorf("cụm: %w", err))
			return 1
		}
		defer base.Cluster.Close()
	}
	runners, err := build(cmd, cfg, log, base)
	if err != nil {
		log.Error("không khởi động được", "err", err)
		fmt.Fprintln(stderr, "\nkhông khởi động được: "+err.Error()) // bản dễ đọc cho người chạy tay
		return 1
	}
	runners = append(runners, migrator.Run)
	if cmd == "ingest" || cmd == "all" {
		runners = append(runners, app.ImportAgentDist(app.Deps{Cfg: cfg, Log: log.With("role", "release"),
			Registry: base.Registry, ReleaseKey: base.ReleaseKey}))
	}
	if err := runAll(ctx, runners); err != nil {
		log.Error("dừng do lỗi", "err", err)
		return 1
	}
	log.Info("đã dừng")
	return 0
}

// loadEnv nạp file chỉ định; không chỉ định thì nạp ./.env nếu có.
func loadEnv(path string) (loaded string, warnings []string, err error) {
	if path == "" {
		if _, err := os.Stat(".env"); err != nil {
			return "", nil, nil
		}
		path = ".env"
	}
	warnings, err = config.LoadDotEnv(path)
	if err != nil {
		return "", warnings, err
	}
	return path, warnings, nil
}

func build(cmd string, cfg *config.Config, log *slog.Logger, base app.Deps) ([]runner, error) {
	var rs []runner
	if cmd == "admin" || cmd == "all" {
		// Số liệu máy server cho trang Tổng quan (chỉ xem). Lệnh all: cổng agent cùng tiến trình nên có
		// cả số heartbeat, sự kiện, kết nối agent; chạy admin riêng thì chỉ có số của máy.
		base.Stats = sysstat.New(nil, cfg.StorageRoot, cmd == "all")
		rs = append(rs, base.Stats.Run)
	}
	if cmd == "ingest" || cmd == "all" {
		if cfg.IngestInsecure() && !cfg.AllowInsecureIngest() {
			return nil, fmt.Errorf("cổng agent %s nghe ra ngoài nhưng thiếu SCS_TLS_CERT hoặc SCS_TLS_CLIENT_CA "+
				"(không mã hóa / không kiểm chứng chỉ agent). Chọn một:\n"+
				"  - khai đủ SCS_TLS_CERT, SCS_TLS_KEY, SCS_TLS_CLIENT_CA;\n"+
				"  - chỉ nghe nội bộ: SCS_INGEST_LISTEN=127.0.0.1:6222;\n"+
				"  - thử nghiệm có chủ ý trên mạng tin cậy: SCS_DEV_INSECURE=1 (prod cấm)", cfg.IngestListen)
		}
		if cfg.DevInsecure {
			log.Warn("SCS_DEV_INSECURE=1: cổng agent không kiểm chứng chỉ, ai vào được cổng cũng gửi dữ liệu được; chỉ dùng thử nghiệm")
		}
		r, err := httpServer("ingest", cfg.IngestListen, cfg, httpx.OptionalClientCert, app.IngestHandler, log, base, nil)
		if err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	if cmd == "admin" || cmd == "all" {
		al := log.With("role", "admin")
		am := auth.NewManager(cfg.AdminToken.Value(), al)
		if as, ok := base.Registry.(auth.AccountStore); ok {
			am.Accounts = as
		}
		am.EmailDomains = cfg.AccountDomains
		if cfg.SMTPHost != "" {
			am.Mail = &auth.SMTP{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser,
				Password: cfg.SMTPPassword.Value(), From: cfg.SMTPFrom}
			al.Info("đăng nhập / đăng ký tài khoản bằng email: bật", "smtp", cfg.SMTPHost, "ten_mien", cfg.AccountDomains)
		} else {
			al.Info("chưa khai SCS_SMTP_HOST: tắt đăng nhập bằng email, chỉ đăng nhập bằng mã lấy qua SSH")
		}
		// Thông báo tức thời cho web quản trị (luồng SSE, hỏi DB vài giây một lần khi có người xem).
		base.Live = app.NewLive(app.Deps{Log: log.With("role", "live"), Registry: base.Registry, Rules: base.Rules})
		rs = append(rs, base.Live.Run)
		r, err := httpServer("admin", cfg.AdminListen, cfg, httpx.NoClientCert, app.AdminHandler, log, base, am)
		if err != nil {
			return nil, err
		}
		// Mã đăng nhập nằm trong RAM; lệnh login-code lấy qua Unix socket quyền 600.
		control := func(ctx context.Context) error { return auth.ServeControl(ctx, cfg.ControlSocket, am, al) }
		rs = append(rs, r, control)
	}
	if cmd == "all" && base.Cluster != nil {
		r, err := clusterServer(cfg, log.With("role", "cluster"), base.Cluster)
		if err != nil {
			return nil, err
		}
		if r != nil {
			rs = append(rs, r)
		}
	}
	if cmd == "worker" || cmd == "all" {
		rs = append(rs, app.Worker(app.Deps{Cfg: cfg, Log: log.With("role", "worker"), Version: version, Registry: base.Registry}))
	}
	return rs, nil
}

func httpServer(role, addr string, cfg *config.Config, mode httpx.ClientAuth,
	handler func(app.Deps) http.Handler, log *slog.Logger, base app.Deps, am *auth.Manager) (runner, error) {
	l := log.With("role", role)
	tlsCfg, err := httpx.ServerTLS(cfg.TLSCert, cfg.TLSKey, cfg.TLSClientCA, mode, l)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", role, err)
	}
	// Web quản trị (xác nhận máy, theo dõi) luôn chạy HTTPS vì có mã đăng nhập và cookie phiên đi qua:
	// chưa có chứng chỉ thật (chỉ xảy ra khi dev, prod đã bắt buộc) thì dùng chứng chỉ tự ký.
	if tlsCfg == nil && role == "admin" {
		var fp string
		if tlsCfg, fp, err = httpx.SelfSignedTLS(); err != nil {
			return nil, fmt.Errorf("%s: tạo chứng chỉ tự ký: %w", role, err)
		}
		l.Warn("chưa khai SCS_TLS_CERT, dùng chứng chỉ tự ký (dev); trình duyệt sẽ cảnh báo, so dấu vân tay trước khi tin",
			"sha256", fp)
	}
	if tlsCfg == nil {
		l.Warn("đang chạy HTTP không mã hóa, chỉ dùng khi dev")
	} else if mode != httpx.NoClientCert && cfg.TLSClientCA == "" {
		l.Warn("chưa khai SCS_TLS_CLIENT_CA, không kiểm chứng chỉ agent (chỉ dùng khi dev)")
	}
	srv := &httpx.Server{
		Name: role,
		Addr: addr,
		Handler: handler(app.Deps{Cfg: cfg, Log: l, Version: version, ReadyChecks: base.ReadyChecks,
			Auth: am, Registry: base.Registry, CA: base.CA, ReleaseKey: base.ReleaseKey, Cluster: base.Cluster, Servers: base.Servers, Rules: base.Rules, Live: base.Live, Stats: base.Stats}),
		TLS: tlsCfg,
		Log: l,
	}
	// Cổng agent mở ra Internet: giới hạn kết nối đang mở (tổng và theo IP) và không giữ
	// lâu kết nối nhàn rỗi (agent gọi mỗi vài phút nên giữ lâu cũng không dùng lại được).
	if role == "ingest" {
		srv.MaxConns, srv.MaxConnsPerIP = cfg.IngestMaxConns, cfg.IngestMaxConnsPerIP
		srv.IdleTimeout = 30 * time.Second
		srv.DisableHTTP2 = true
		if base.Stats != nil {
			srv.Conns = &base.Stats.Act.IngestConns
		}
		l.Info("giới hạn kết nối", "tong", cfg.IngestMaxConns, "moi_ip", cfg.IngestMaxConnsPerIP)
	}
	// Web quản trị chỉ cho VPN / máy server truy cập (SCS_ADMIN_ALLOW).
	if role == "admin" {
		srv.Allow = cfg.AdminAllow
		list, loopback := make([]string, len(cfg.AdminAllow)), false
		for i, p := range cfg.AdminAllow {
			list[i] = p.String()
			if p.Bits() == 0 {
				l.Warn("SCS_ADMIN_ALLOW đang cho phép mọi IP; nên giới hạn ở dải VPN", "dai", p.String())
			}
			if p.Addr().IsLoopback() {
				loopback = true
			}
		}
		l.Info("chỉ nhận kết nối từ", "SCS_ADMIN_ALLOW", strings.Join(list, ", "))
		if loopback {
			l.Info("danh sách có loopback: mọi kết nối xuất phát từ chính máy server đều được vào " +
				"(SSH tunnel, port-forward, proxy); đừng chạy proxy hay chuyển tiếp cổng 7222 trên máy này")
		}
	}
	return srv.Run, nil
}

// runAll chạy song song; một runner lỗi thì hủy các runner còn lại và trả lỗi đầu tiên.
func runAll(ctx context.Context, rs []runner) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, r := range rs {
		wg.Add(1)
		go func(r runner) {
			defer wg.Done()
			if err := r(ctx); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				cancel()
			}
		}(r)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func newLogger(w io.Writer, level string) *slog.Logger {
	var lv slog.Level
	lv.UnmarshalText([]byte(level)) // config.Load đã kiểm giá trị hợp lệ
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv}))
}

// loginCode in mã đăng nhập web quản trị lấy từ server đang chạy.
func loginCode(socket string, watch bool, stdout, stderr io.Writer) int {
	cur, err := auth.FetchCodes(socket)
	if err != nil {
		fmt.Fprintf(stderr, "Không lấy được mã từ server qua %s: %v\n"+
			"Kiểm tra: server đang chạy lệnh admin hoặc all, lệnh này chạy bằng cùng user với dịch vụ,\n"+
			"và cùng SCS_CONTROL_SOCKET (cùng thư mục hoặc cùng file .env).\n", socket, err)
		return 1
	}
	printCodes(stdout, cur)
	if !watch {
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-t.C:
		}
		next, err := auth.FetchCodes(socket)
		if err != nil {
			fmt.Fprintln(stderr, "mất kết nối tới server:", err)
			return 1
		}
		if next != cur {
			cur = next
			printCodes(stdout, cur)
		}
	}
}

func printCodes(w io.Writer, c auth.Current) {
	left := int(time.Until(c.Until).Seconds())
	spaced := func(s string) string {
		if len(s) == 6 {
			return s[:3] + " " + s[3:]
		}
		return s
	}
	fmt.Fprintf(w, "\nMã đăng nhập web quản trị (mỗi mã dùng 1 lần; đổi lúc %s, còn %d giây)\n",
		c.Until.Local().Format("15:04:05"), max(0, left))
	fmt.Fprintf(w, "  Admin    : %s\n  Chỉ xem  : %s\n", spaced(c.Admin), spaced(c.Viewer))
	if time.Now().Before(c.LockedUntil) {
		fmt.Fprintf(w, "  CHÚ Ý: đăng nhập đang bị KHÓA tới %s do nhập sai mã quá nhiều (có thể đang bị dò mã).\n"+
			"  Mở khóa: scs-server login-code -unlock\n", c.LockedUntil.Local().Format("15:04:05"))
	}
}

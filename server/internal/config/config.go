// Package config đọc cấu hình server từ biến môi trường (thường nạp từ .env).
//
// Thứ tự ưu tiên: biến môi trường có sẵn > file .env > giá trị mặc định.
// Mật khẩu và token chỉ nằm trong .env / biến môi trường, không ghi vào code.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Env      string // dev | prod
	LogLevel string // debug | info | warn | error
	// DevInsecure (SCS_DEV_INSECURE=1): cho cổng agent chạy không TLS / không kiểm chứng chỉ
	// agent khi nghe ngoài loopback. Chỉ để thử nghiệm có chủ ý; prod cấm bật.
	DevInsecure bool

	IngestListen string
	AdminListen  string
	// Giới hạn kết nối đang mở trên cổng agent (chống mở hàng loạt kết nối để chiếm RAM).
	// Theo IP phải đủ cho một cửa hàng nhiều máy sau cùng IP WAN.
	IngestMaxConns      int
	IngestMaxConnsPerIP int
	// AdminAllow: dải IP được kết nối vào web quản trị (VPN, máy server).
	// Kết nối từ IP khác bị đóng ngay, trước khi bắt tay TLS.
	AdminAllow []netip.Prefix

	TLSCert     string
	TLSKey      string
	TLSClientCA string
	// CACert, CAKey: CA nội bộ cấp chứng chỉ cho agent khi admin duyệt (tạo bằng scs-server pki init).
	CACert string
	CAKey  string

	DB          DB
	NATSURL     string
	NATSUser    string
	NATSPass    secret
	StorageRoot string
	// AdminToken: Bearer token cho script tự động gọi API quản trị (tùy chọn).
	// Người dùng web đăng nhập bằng mã 6 số, không dùng token này.
	AdminToken secret
	// ControlSocket: Unix socket để lệnh `scs-server login-code` lấy mã đăng nhập.
	ControlSocket string
}

type DB struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password secret
	SSLMode  string
}

// secret không bao giờ in ra giá trị thật khi format bằng fmt/log.
type secret string

func (s secret) String() string {
	if s == "" {
		return ""
	}
	return "******"
}

func (s secret) GoString() string { return s.String() }

// Value trả về giá trị thật; chỉ gọi đúng chỗ cần dùng (kết nối DB, so token).
func (s secret) Value() string { return string(s) }

// Load đọc cấu hình từ môi trường và kiểm tra hợp lệ.
func Load() (*Config, error) {
	var errs []error
	c := &Config{
		Env:      get("SCS_ENV", "dev"),
		LogLevel: get("SCS_LOG_LEVEL", "info"),

		DevInsecure: getBool("SCS_DEV_INSECURE", &errs),

		IngestListen: get("SCS_INGEST_LISTEN", ":6222"),
		AdminListen:  get("SCS_ADMIN_LISTEN", ":7222"),

		IngestMaxConns:      getInt("SCS_INGEST_MAX_CONNS", 40000, &errs),
		IngestMaxConnsPerIP: getInt("SCS_INGEST_MAX_CONNS_PER_IP", 64, &errs),
		AdminAllow:          getPrefixes("SCS_ADMIN_ALLOW", "127.0.0.0/8,::1", &errs),

		TLSCert:     get("SCS_TLS_CERT", ""),
		TLSKey:      get("SCS_TLS_KEY", ""),
		TLSClientCA: get("SCS_TLS_CLIENT_CA", ""),
		CACert:      get("SCS_CA_CERT", ""),
		CAKey:       get("SCS_CA_KEY", ""),

		DB: DB{
			Host:     get("SCS_DB_HOST", "127.0.0.1"),
			Port:     getInt("SCS_DB_PORT", 5432, &errs),
			Name:     get("SCS_DB_NAME", "scs"),
			User:     get("SCS_DB_USER", "scs"),
			Password: secret(get("SCS_DB_PASSWORD", "")),
			SSLMode:  get("SCS_DB_SSLMODE", "disable"),
		},
		NATSURL:     get("SCS_NATS_URL", "nats://127.0.0.1:4222"),
		NATSUser:    get("SCS_NATS_USER", ""),
		NATSPass:    secret(get("SCS_NATS_PASSWORD", "")),
		StorageRoot: get("SCS_STORAGE_ROOT", "./data/devices"),
		AdminToken:  secret(get("SCS_ADMIN_TOKEN", "")),

		ControlSocket: get("SCS_CONTROL_SOCKET", "data/run/control.sock"),
	}
	errs = append(errs, c.validate()...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

func (c *Config) IsProd() bool { return c.Env == "prod" }

// IngestInsecure: cổng agent đang thiếu TLS hoặc thiếu CA kiểm chứng chỉ agent.
func (c *Config) IngestInsecure() bool { return c.TLSCert == "" || c.TLSClientCA == "" }

// AllowInsecureIngest cho biết được phép chạy cổng agent khi IngestInsecure.
// Mặc định là KHÔNG (an toàn khi quên đặt SCS_ENV=prod): chỉ cho khi dev và hoặc
// cổng chỉ nghe loopback, hoặc đã bật rõ SCS_DEV_INSECURE=1.
func (c *Config) AllowInsecureIngest() bool {
	return !c.IsProd() && (c.DevInsecure || loopbackListen(c.IngestListen))
}

// DatabaseURL ghép chuỗi kết nối PostgreSQL; mật khẩu được escape nên
// chứa ký tự đặc biệt (@ : / #) vẫn đúng.
func (c *Config) DatabaseURL() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.DB.User, c.DB.Password.Value()),
		Host:     fmt.Sprintf("%s:%d", c.DB.Host, c.DB.Port),
		Path:     "/" + c.DB.Name,
		RawQuery: url.Values{"sslmode": {c.DB.SSLMode}}.Encode(),
	}
	return u.String()
}

func (c *Config) validate() []error {
	var errs []error
	switch c.Env {
	case "dev", "prod":
	default:
		errs = append(errs, fmt.Errorf("SCS_ENV phải là dev hoặc prod, đang là %q", c.Env))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("SCS_LOG_LEVEL không hợp lệ: %q", c.LogLevel))
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		errs = append(errs, errors.New("SCS_TLS_CERT và SCS_TLS_KEY phải khai cùng nhau"))
	}
	if (c.CACert == "") != (c.CAKey == "") {
		errs = append(errs, errors.New("SCS_CA_CERT và SCS_CA_KEY phải khai cùng nhau"))
	}
	if c.IngestMaxConns < 1 || c.IngestMaxConnsPerIP < 1 || c.IngestMaxConnsPerIP > c.IngestMaxConns {
		errs = append(errs, errors.New("SCS_INGEST_MAX_CONNS và SCS_INGEST_MAX_CONNS_PER_IP phải ≥ 1, và theo IP không lớn hơn tổng"))
	}
	if len(c.AdminToken) > 0 && len(c.AdminToken) < 32 {
		errs = append(errs, errors.New("SCS_ADMIN_TOKEN phải dài ít nhất 32 ký tự"))
	}
	if c.IsProd() {
		if c.DevInsecure {
			errs = append(errs, errors.New("prod không được bật SCS_DEV_INSECURE"))
		}
		if c.TLSCert == "" || c.TLSClientCA == "" {
			errs = append(errs, errors.New("prod bắt buộc SCS_TLS_CERT, SCS_TLS_KEY, SCS_TLS_CLIENT_CA"))
		}
		if c.CACert == "" {
			errs = append(errs, errors.New("prod bắt buộc SCS_CA_CERT, SCS_CA_KEY (CA cấp chứng chỉ agent)"))
		}
		if c.DB.Password == "" {
			errs = append(errs, errors.New("prod bắt buộc SCS_DB_PASSWORD"))
		}
		if c.DB.SSLMode == "disable" && !isLocalHost(c.DB.Host) {
			errs = append(errs, errors.New("DB ở máy khác thì SCS_DB_SSLMODE không được là disable"))
		}
	}
	return errs
}

func get(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getBool(key string, errs *[]error) bool {
	switch strings.ToLower(get(key, "")) {
	case "", "0", "false", "no":
		return false
	case "1", "true", "yes":
		return true
	}
	*errs = append(*errs, fmt.Errorf("%s phải là 0 hoặc 1", key))
	return false
}

func getInt(key string, def int, errs *[]error) int {
	v := get(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s phải là số, đang là %q", key, v))
		return def
	}
	return n
}

// getPrefixes đọc danh sách "10.8.0.0/24, 100.64.0.0/10, 192.168.1.5" (IP đơn = /32 hoặc /128).
func getPrefixes(key, def string, errs *[]error) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range strings.Split(get(key, def), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(s); err == nil {
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		*errs = append(*errs, fmt.Errorf("%s: %q không phải IP hoặc dải CIDR hợp lệ", key, s))
	}
	if len(out) == 0 {
		*errs = append(*errs, fmt.Errorf("%s không được rỗng (mặc định: %s)", key, def))
	}
	return out
}

// loopbackListen: địa chỉ lắng nghe chỉ ở loopback ("127.0.0.1:6222", "[::1]:6222", "localhost:6222").
// ":6222" hay "0.0.0.0:6222" là nghe mọi card mạng.
func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func isLocalHost(h string) bool {
	h = strings.ToLower(h)
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasPrefix(h, "/")
}

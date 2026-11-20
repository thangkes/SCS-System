// Package config đọc cấu hình agent từ /etc/scs-agent/agent.env và biến môi trường.
//
// Thứ tự ưu tiên: biến môi trường có sẵn > file agent.env > giá trị mặc định.
// Agent không chứa bí mật dùng chung nào: file cấu hình chỉ có địa chỉ server và tùy chọn.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// DefaultFile là file cấu hình khi cài bằng gói .deb.
const DefaultFile = "/etc/scs-agent/agent.env"

type Config struct {
	// Server: địa chỉ cổng agent của server, dạng https://<IP WAN>:<cổng>. Router phía
	// server chuyển tiếp cổng này về 6222 của scs-server.
	Server string
	// ServerCA: CA dùng để tin chứng chỉ server ngay từ lần đăng ký đầu (đóng sẵn trong gói).
	ServerCA string
	// KeyDir: khóa riêng, chứng chỉ của máy (quyền 600). StateDir: trạng thái, vị trí đọc log, hàng đợi.
	KeyDir   string
	StateDir string
	// WatchServices: dịch vụ systemd cần báo trạng thái trong heartbeat (ví dụ dịch vụ POS).
	WatchServices []string
	LogLevel      string
	// ReleasePub: khóa công khai phát hành của server (root sở hữu, đóng trong gói cài). Agent chỉ
	// chạy bản cập nhật có chữ ký đúng khóa này; không có file thì không tự cập nhật.
	ReleasePub string
	// Probes: cho phép agent tự kiểm thiết bị trong LAN khi bộ rule đã ký yêu cầu (rule nguồn probe).
	// SCS_PROBES=off tắt hẳn trên máy này (file agent.env do root giữ, server không đổi được). Mặc định bật.
	Probes bool
}

// Load đọc file cấu hình (path rỗng = DefaultFile, không có file thì dùng mặc định) rồi biến môi trường.
func Load(path string) (*Config, error) {
	explicit := path != ""
	if !explicit {
		path = DefaultFile
	}
	file := map[string]string{}
	if f, err := os.Open(path); err == nil {
		file, err = parse(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else if explicit || !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("đọc cấu hình: %w", err)
	}
	get := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		if v := file[key]; v != "" {
			return v
		}
		return def
	}
	c := &Config{
		Server:   get("SCS_SERVER", ""),
		ServerCA: get("SCS_SERVER_CA", "/etc/scs-agent/server-ca.pem"),
		KeyDir:   get("SCS_KEY_DIR", "/etc/scs-agent"),
		StateDir: get("SCS_STATE_DIR", "/var/lib/scs-agent"),
		LogLevel: get("SCS_LOG_LEVEL", "info"),

		ReleasePub: get("SCS_RELEASE_PUB", "/opt/scs-agent/release.pub"),
	}
	switch strings.ToLower(get("SCS_PROBES", "on")) {
	case "on", "1", "true", "yes":
		c.Probes = true
	case "off", "0", "false", "no":
	default:
		return nil, fmt.Errorf("SCS_PROBES không hợp lệ (on | off): %q", get("SCS_PROBES", ""))
	}
	for _, s := range strings.Split(get("SCS_WATCH_SERVICES", ""), ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.WatchServices = append(c.WatchServices, s)
		}
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Tên dịch vụ systemd hợp lệ; không cho bắt đầu bằng "-" để không thành tham số của systemctl.
var serviceName = regexp.MustCompile(`^[A-Za-z0-9@_.:\\][A-Za-z0-9@_.:\\-]{0,127}$`)

// ValidServiceName: tên dịch vụ systemd hợp lệ (dùng cả cho cấu hình server gửi xuống).
func ValidServiceName(s string) bool { return serviceName.MatchString(s) }

func (c *Config) validate() error {
	var errs []error
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("SCS_LOG_LEVEL không hợp lệ: %q", c.LogLevel))
	}
	if c.Server != "" {
		if err := checkServer(c.Server); err != nil {
			errs = append(errs, err)
		}
	}
	for _, s := range c.WatchServices {
		if !serviceName.MatchString(s) {
			errs = append(errs, fmt.Errorf("SCS_WATCH_SERVICES: %q không phải tên dịch vụ systemd hợp lệ", s))
		}
	}
	if len(c.WatchServices) > 32 {
		errs = append(errs, errors.New("SCS_WATCH_SERVICES: tối đa 32 dịch vụ"))
	}
	return errors.Join(errs...)
}

// RequireServer: lệnh cần gọi server (run, enroll) phải có SCS_SERVER.
func (c *Config) RequireServer() error {
	if c.Server == "" {
		return fmt.Errorf("chưa khai SCS_SERVER trong %s (ví dụ SCS_SERVER=https://203.0.113.10:6222)", DefaultFile)
	}
	return nil
}

// checkServer: chỉ nhận https://<host>:<cổng>, không có đường dẫn, user hay query.
func checkServer(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("SCS_SERVER phải có dạng https://<IP>:<cổng>, đang là %q", s)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("SCS_SERVER phải ghi rõ cổng, ví dụ https://203.0.113.10:6222 (đang là %q)", s)
	}
	return nil
}

// parse đọc KEY=VALUE, bỏ dòng trống và dòng chú thích; giá trị có thể đặt trong ngoặc kép.
func parse(f *os.File) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("dòng %d không có dạng KEY=VALUE", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		out[k] = v
	}
	return out, sc.Err()
}

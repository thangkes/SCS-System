package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearEnv(t *testing.T) {
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "SCS_") {
			t.Setenv(k, "")
		}
	}
}

func writeConf(t *testing.T, s string) string {
	p := filepath.Join(t.TempDir(), "agent.env")
	os.WriteFile(p, []byte(s), 0o640)
	return p
}

func TestLoadFileAndEnv(t *testing.T) {
	clearEnv(t)
	p := writeConf(t, `# cấu hình agent
SCS_SERVER=https://203.0.113.10:16222
SCS_WATCH_SERVICES="pos.service, cups.service"
export SCS_LOG_LEVEL=debug   # ghi chi tiết
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "https://203.0.113.10:16222" || c.LogLevel != "debug" ||
		len(c.WatchServices) != 2 || c.WatchServices[1] != "cups.service" || c.KeyDir != "/etc/scs-agent" {
		t.Fatalf("cấu hình = %+v", c)
	}
	t.Setenv("SCS_SERVER", "https://198.51.100.7:443")
	if c, _ := Load(p); c.Server != "https://198.51.100.7:443" {
		t.Fatal("biến môi trường phải thắng file")
	}
}

// SCS_PROBES: mặc định bật; off tắt hẳn kiểm thiết bị trên máy; giá trị lạ bị từ chối.
func TestProbesSwitch(t *testing.T) {
	clearEnv(t)
	p := writeConf(t, "SCS_SERVER=https://203.0.113.10:16222\n")
	if c, err := Load(p); err != nil || !c.Probes {
		t.Fatalf("mặc định bật: %+v %v", c, err)
	}
	p = writeConf(t, "SCS_SERVER=https://203.0.113.10:16222\nSCS_PROBES=off\n")
	if c, err := Load(p); err != nil || c.Probes {
		t.Fatalf("off phải tắt: %+v %v", c, err)
	}
	p = writeConf(t, "SCS_PROBES=maybe\n")
	if _, err := Load(p); err == nil {
		t.Fatal("giá trị lạ phải bị từ chối")
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"http thường":         "SCS_SERVER=http://203.0.113.10:6222",
		"thiếu cổng":          "SCS_SERVER=https://203.0.113.10",
		"có đường dẫn":        "SCS_SERVER=https://203.0.113.10:6222/v1",
		"có user":             "SCS_SERVER=https://a:b@203.0.113.10:6222",
		"dịch vụ như tham số": "SCS_WATCH_SERVICES=--now",
		"dịch vụ có khoảng":   `SCS_WATCH_SERVICES="pos service"`,
		"dịch vụ có ;":        "SCS_WATCH_SERVICES=pos;reboot",
		"mức log lạ":          "SCS_LOG_LEVEL=verbose",
		"dòng không có dấu =": "SCS_SERVER",
	}
	for name, conf := range cases {
		clearEnv(t)
		if _, err := Load(writeConf(t, conf)); err == nil {
			t.Errorf("%s: phải lỗi", name)
		}
	}
	clearEnv(t)
	if _, err := Load(filepath.Join(t.TempDir(), "khong-co.env")); err == nil {
		t.Error("chỉ định file không tồn tại phải lỗi")
	}
	c, err := Load(writeConf(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if c.RequireServer() == nil {
		t.Error("chưa có SCS_SERVER thì lệnh run phải báo lỗi")
	}
}

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	src := string(rune(0xFEFF)) + "SCS_BOM=co_bom\n" +
		"\n" +
		"SCS_ENV=dev\n" +
		"export SCS_LOG_LEVEL=debug   # chú thích cuối dòng\n" +
		"SCS_DB_PASSWORD=\"p@ss w0rd#1 \\\"x\\\"\"\n" +
		"SCS_NATS_PASSWORD='nguyen$van\\n'\n" +
		"SCS_EMPTY=\n" +
		"SCS_EMPTY_COMMENT=          # chỉ có chú thích\n" +
		"SCS_HASH=abc#def\n"
	got, err := parseDotEnv(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"SCS_BOM":           "co_bom", // dòng đầu có BOM vẫn đọc đúng tên biến
		"SCS_ENV":           "dev",
		"SCS_LOG_LEVEL":     "debug",
		"SCS_DB_PASSWORD":   `p@ss w0rd#1 "x"`,
		"SCS_NATS_PASSWORD": `nguyen$van\n`,
		"SCS_EMPTY":         "",
		"SCS_EMPTY_COMMENT": "",
		"SCS_HASH":          "abc#def",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, muốn %q", k, got[k], v)
		}
	}
}

func TestParseDotEnvErrorsHideValue(t *testing.T) {
	for _, src := range []string{"KHONG_CO_DAU_BANG\n", "1BAD=x\n", "SCS_DB_PASSWORD=\"bimat\n"} {
		_, err := parseDotEnv(strings.NewReader(src))
		if err == nil {
			t.Errorf("%q phải lỗi", src)
			continue
		}
		if strings.Contains(err.Error(), "bimat") {
			t.Errorf("lỗi không được in giá trị: %v", err)
		}
	}
}

func TestLoadDotEnvDoesNotOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(path, []byte("SCS_T_A=tu_file\nSCS_T_B=tu_file\n"), 0o600)
	t.Setenv("SCS_T_A", "tu_moi_truong")
	os.Unsetenv("SCS_T_B")
	t.Cleanup(func() { os.Unsetenv("SCS_T_B") })

	if _, err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if v := os.Getenv("SCS_T_A"); v != "tu_moi_truong" {
		t.Errorf("biến môi trường có sẵn bị ghi đè: %q", v)
	}
	if v := os.Getenv("SCS_T_B"); v != "tu_file" {
		t.Errorf("biến từ .env không được nạp: %q", v)
	}
}

func clearEnv(t *testing.T) {
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "SCS_") {
			t.Setenv(k, "")
		}
	}
}

func TestLoadDefaultsDev(t *testing.T) {
	clearEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != "dev" || c.IngestListen != ":6222" || c.AdminListen != ":7222" || c.DB.Port != 5432 {
		t.Fatalf("mặc định sai: %+v", c)
	}
}

func TestProdRequiresSecrets(t *testing.T) {
	clearEnv(t)
	t.Setenv("SCS_ENV", "prod")
	_, err := Load()
	if err == nil {
		t.Fatal("prod thiếu TLS/mật khẩu/token phải lỗi")
	}
	for _, s := range []string{"SCS_TLS_CERT", "SCS_DB_PASSWORD"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("lỗi phải nhắc %s: %v", s, err)
		}
	}
}

func TestDevInsecure(t *testing.T) {
	clearEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.IngestInsecure() || c.AllowInsecureIngest() {
		t.Fatal("mặc định (dev, :6222, không TLS) phải KHÔNG được phép chạy không kiểm chứng chỉ")
	}
	t.Setenv("SCS_DEV_INSECURE", "1")
	if c, err = Load(); err != nil || !c.AllowInsecureIngest() {
		t.Fatalf("bật rõ SCS_DEV_INSECURE=1 thì được: %v", err)
	}
	t.Setenv("SCS_ENV", "prod")
	if _, err = Load(); err == nil || !strings.Contains(err.Error(), "SCS_DEV_INSECURE") {
		t.Fatalf("prod bật SCS_DEV_INSECURE phải lỗi: %v", err)
	}
	t.Setenv("SCS_ENV", "")
	t.Setenv("SCS_DEV_INSECURE", "co")
	if _, err = Load(); err == nil {
		t.Fatal("SCS_DEV_INSECURE không phải 0/1 phải lỗi")
	}

	for addr, want := range map[string]bool{
		"127.0.0.1:6222": true, "[::1]:6222": true, "localhost:6222": true, "127.0.0.5:1": true,
		":6222": false, "0.0.0.0:6222": false, "[::]:6222": false, "10.0.0.1:6222": false, "6222": false,
	} {
		if got := loopbackListen(addr); got != want {
			t.Errorf("loopbackListen(%q) = %v, muốn %v", addr, got, want)
		}
	}
}

func TestIngestConnLimits(t *testing.T) {
	clearEnv(t)
	c, err := Load()
	if err != nil || c.IngestMaxConns != 40000 || c.IngestMaxConnsPerIP != 64 {
		t.Fatalf("mặc định sai: %v %+v", err, c)
	}
	for _, kv := range [][2]string{{"SCS_INGEST_MAX_CONNS", "0"}, {"SCS_INGEST_MAX_CONNS_PER_IP", "50000"}, {"SCS_INGEST_MAX_CONNS", "abc"}} {
		clearEnv(t)
		t.Setenv(kv[0], kv[1])
		if _, err := Load(); err == nil {
			t.Errorf("%s=%s phải lỗi", kv[0], kv[1])
		}
	}
}

func TestSecretsNotPrinted(t *testing.T) {
	clearEnv(t)
	t.Setenv("SCS_DB_PASSWORD", "MatKhauRatBiMat")
	t.Setenv("SCS_ADMIN_TOKEN", strings.Repeat("t", 40))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(out, "MatKhauRatBiMat") || strings.Contains(out, strings.Repeat("t", 40)) {
			t.Fatalf("lộ secret khi in config: %s", out)
		}
	}
}

func TestDatabaseURLEscapesPassword(t *testing.T) {
	clearEnv(t)
	t.Setenv("SCS_DB_PASSWORD", "a@b:c/d#e")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := "postgres://scs:a%40b%3Ac%2Fd%23e@127.0.0.1:5432/scs?sslmode=disable"
	if got := c.DatabaseURL(); got != want {
		t.Fatalf("DatabaseURL = %s\nmuốn %s", got, want)
	}
}

func TestShortAdminToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("SCS_ADMIN_TOKEN", "ngan")
	if _, err := Load(); err == nil {
		t.Fatal("token ngắn phải lỗi")
	}
}

func TestAdminAllow(t *testing.T) {
	clearEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(c.AdminAllow); got != "[127.0.0.0/8 ::1/128]" {
		t.Fatalf("mặc định chỉ được localhost, được %s", got)
	}

	t.Setenv("SCS_ADMIN_ALLOW", " 10.8.0.5/24 , 100.64.0.0/10,192.168.1.5, ::1 ")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(c.AdminAllow); got != "[10.8.0.0/24 100.64.0.0/10 192.168.1.5/32 ::1/128]" {
		t.Fatalf("đọc sai danh sách: %s", got)
	}

	for _, bad := range []string{"10.8.0.0/33", "abc", "10.8.0.0/24,khong-phai-ip", " , "} {
		t.Setenv("SCS_ADMIN_ALLOW", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SCS_ADMIN_ALLOW") {
			t.Errorf("%q phải báo lỗi SCS_ADMIN_ALLOW, được %v", bad, err)
		}
	}
}

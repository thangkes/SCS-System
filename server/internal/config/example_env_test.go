package config

import (
	"os"
	"testing"
)

// .env.example phải đọc được, và chép nguyên thành .env vẫn khởi động được ở chế độ dev.
func TestEnvExampleLoads(t *testing.T) {
	f, err := os.Open("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	vars, err := parseDotEnv(f)
	if err != nil {
		t.Fatal(err)
	}
	clearEnv(t)
	for k, v := range vars {
		if len(v) > 0 && v[0] == '#' {
			t.Errorf("%s bị đọc nhầm chú thích thành giá trị: %q", k, v)
		}
		t.Setenv(k, v)
	}
	if _, err := Load(); err != nil {
		t.Fatalf(".env.example không hợp lệ: %v", err)
	}
}

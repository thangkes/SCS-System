package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCommands(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 || strings.TrimSpace(out.String()) != version {
		t.Fatalf("version: %d %q", code, out.String())
	}
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatalf("không có lệnh: %d", code)
	}
	if code := run([]string{"xoa"}, &out, &errb); code != 2 {
		t.Fatalf("lệnh lạ: %d", code)
	}
}

func TestFingerprintAndStatusDoNotCreateKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCS_KEY_DIR", dir+"/key")
	t.Setenv("SCS_STATE_DIR", dir+"/state")
	t.Setenv("SCS_SERVER", "")
	var out, errb bytes.Buffer
	if code := run([]string{"fingerprint"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "chưa có") {
		t.Fatalf("fingerprint: %d\n%s%s", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"status"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "Trạng thái đăng ký: new") {
		t.Fatalf("status: %d\n%s%s", code, out.String(), errb.String())
	}
	out.Reset()
	if run([]string{"fingerprint"}, &out, &errb); !strings.Contains(out.String(), "chưa có") {
		t.Fatal("lệnh xem thông tin không được tạo khóa")
	}
}

func TestRunNeedsServer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCS_KEY_DIR", dir+"/key")
	t.Setenv("SCS_STATE_DIR", dir+"/state")
	t.Setenv("SCS_SERVER", "")
	var out, errb bytes.Buffer
	if code := run([]string{"run"}, &out, &errb); code != 1 || !strings.Contains(out.String(), "SCS_SERVER") {
		t.Fatalf("run thiếu SCS_SERVER: %d %s", code, out.String())
	}
}

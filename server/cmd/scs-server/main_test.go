package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/server/internal/pki"
)

func TestRunCommands(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 || strings.TrimSpace(out.String()) != version {
		t.Fatalf("version: code=%d out=%q", code, out.String())
	}
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatalf("không có lệnh: code=%d, muốn 2", code)
	}
	errb.Reset()
	if code := run([]string{"xoa-het"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "lệnh không hợp lệ") {
		t.Fatalf("lệnh lạ: code=%d err=%q", code, errb.String())
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	t.Setenv("SCS_ENV", "prod") // prod thiếu TLS/mật khẩu → không được khởi động
	var out, errb bytes.Buffer
	if code := run([]string{"ingest"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "SCS_TLS_CERT") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

// Quên SCS_ENV=prod: dev mặc định, cổng agent nghe mọi card mạng, không TLS → không được chạy.
func TestRunRefusesInsecureIngest(t *testing.T) {
	for _, k := range []string{"SCS_ENV", "SCS_DEV_INSECURE", "SCS_TLS_CERT", "SCS_TLS_KEY", "SCS_TLS_CLIENT_CA"} {
		t.Setenv(k, "")
	}
	t.Setenv("SCS_INGEST_LISTEN", ":6222")
	var out, errb bytes.Buffer
	if code := run([]string{"ingest"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "SCS_DEV_INSECURE=1") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRunMissingEnvFile(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"ingest", "-env-file", "khong-ton-tai.env"}, &out, &errb); code != 1 {
		t.Fatalf("file .env chỉ định mà không có phải lỗi, code=%d", code)
	}
}

func TestRunAllStopsOthersOnError(t *testing.T) {
	boom := errors.New("không mở được cổng")
	stopped := make(chan struct{})
	start := time.Now()
	err := runAll(context.Background(), []runner{
		func(ctx context.Context) error { return boom },
		func(ctx context.Context) error { <-ctx.Done(); close(stopped); return nil },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("runner còn lại phải được dừng")
	}
	if time.Since(start) > time.Second {
		t.Fatal("dừng quá chậm")
	}
}

func TestPKIInit(t *testing.T) {
	dir := t.TempDir() + "/pki"
	var out, errb bytes.Buffer
	if code := run([]string{"pki", "init", "-ip", "203.0.113.10", "-ip", "10.8.0.1", "-dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("pki init: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "SCS_CA_KEY=") || !strings.Contains(out.String(), "203.0.113.10, 10.8.0.1") {
		t.Fatalf("phải in hướng dẫn cấu hình: %s", out.String())
	}
	ca, err := pki.Load(dir+"/ca.pem", dir+"/ca.key")
	if err != nil {
		t.Fatalf("CA vừa tạo phải đọc lại được: %v", err)
	}
	if _, err := tls.LoadX509KeyPair(dir+"/server.pem", dir+"/server.key"); err != nil || ca == nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := run([]string{"pki", "init", "-ip", "203.0.113.10", "-dir", dir}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "không ghi đè") {
		t.Fatalf("chạy lại init không được ghi đè CA: %d %s", code, errb.String())
	}
	if code := run([]string{"pki", "init", "-dir", dir + "2"}, &out, &errb); code != 2 {
		t.Fatal("thiếu -ip phải báo lỗi")
	}
	if code := run([]string{"pki", "init", "-ip", "abc", "-dir", dir + "3"}, &out, &errb); code != 2 {
		t.Fatal("-ip sai phải báo lỗi")
	}
}

package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// DB không chạy: Open vẫn thành công (kết nối lười), ReadyCheck báo lỗi nhanh.
func TestReadyCheckUnreachable(t *testing.T) {
	ctx := context.Background()
	p, err := Open(ctx, "postgres://scs:MatKhauBiMat@127.0.0.1:1/scs?sslmode=disable")
	if err != nil {
		t.Fatalf("Open không được lỗi khi DB chưa lên: %v", err)
	}
	defer p.Close()

	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	err = ReadyCheck(p).Fn(cctx)
	if err == nil {
		t.Fatal("DB không chạy mà ReadyCheck không lỗi")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("ReadyCheck phải tôn trọng timeout")
	}
	if strings.Contains(err.Error(), "MatKhauBiMat") {
		t.Fatalf("lỗi chứa mật khẩu: %v", err)
	}
}

func TestOpenBadURLHidesSecret(t *testing.T) {
	_, err := Open(context.Background(), "postgres://scs:MatKhauBiMat@127.0.0.1:5432/scs?sslmode=khong-hop-le")
	if err == nil {
		t.Fatal("chuỗi kết nối sai phải lỗi")
	}
	if strings.Contains(err.Error(), "MatKhauBiMat") {
		t.Fatalf("lỗi chứa mật khẩu: %v", err)
	}
}

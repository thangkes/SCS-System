package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/thangkes/SCS-System/server/internal/auth"
	"github.com/thangkes/SCS-System/server/internal/config"
)

var mailTestTo = regexp.MustCompile(`^[a-z0-9._%+-]{1,64}@[a-z0-9.-]{1,190}\.[a-z]{2,24}$`)

// runMailTest: `scs-server mail-test -to <email>` gửi thử một mail bằng cấu hình SCS_SMTP_* để biết
// Gmail gửi mã (địa chỉ, mật khẩu ứng dụng) đã đúng chưa, không cần đăng ký tài khoản.
func runMailTest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mail-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	envFile := fs.String("env-file", "", "file .env cần nạp (server đã cài: /etc/scs-server/server.env)")
	to := fs.String("to", "", "email nhận thư thử")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	addr := strings.ToLower(strings.TrimSpace(*to))
	if !mailTestTo.MatchString(addr) {
		fmt.Fprintln(stderr, "cần -to <email nhận thư thử>")
		return 2
	}
	if _, _, err := loadEnv(*envFile); err != nil {
		fmt.Fprintln(stderr, "lỗi đọc .env:", err)
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "cấu hình không hợp lệ:\n"+err.Error())
		return 1
	}
	if cfg.SMTPHost == "" {
		fmt.Fprintln(stderr, "chưa khai SCS_SMTP_HOST (đăng nhập bằng email đang tắt)")
		return 1
	}
	m := &auth.SMTP{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser,
		Password: cfg.SMTPPassword.Value(), From: cfg.SMTPFrom}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := m.Send(ctx, addr, "SCS-System: thư thử",
		"Thư thử từ SCS-Server: cấu hình gửi mã đăng nhập qua email đã hoạt động.\n"); err != nil {
		fmt.Fprintln(stderr, "gửi thất bại:", err)
		if strings.Contains(err.Error(), "535") || strings.Contains(strings.ToLower(err.Error()), "username and password") {
			fmt.Fprintln(stderr, "Gmail từ chối đăng nhập: kiểm SCS_SMTP_USER và dùng Mật khẩu ứng dụng (16 chữ), không phải mật khẩu Gmail.")
		}
		return 1
	}
	fmt.Fprintf(stdout, "Đã gửi thư thử tới %s qua %s (người gửi %s). Không thấy thì xem thư mục Spam.\n", addr, cfg.SMTPHost, cfg.SMTPUser)
	return 0
}

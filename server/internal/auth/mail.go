package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTP gửi mail qua máy chủ SMTP có STARTTLS (Gmail: smtp.gmail.com:587, đăng nhập bằng mật khẩu ứng
// dụng của một tài khoản Gmail riêng để gửi). Bắt buộc TLS: không gửi mật khẩu khi máy chủ không hỗ trợ.
type SMTP struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string // địa chỉ người gửi (thường = User)
	Timeout  time.Duration
}

// Send gửi một email chữ thường (UTF-8).
func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	if s.Host == "" || s.User == "" || s.Password == "" {
		return errors.New("chưa cấu hình SMTP")
	}
	if strings.ContainsAny(to, "\r\n<>") || strings.ContainsAny(subject, "\r\n") {
		return errors.New("địa chỉ / tiêu đề không hợp lệ")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("không kết nối được %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return errors.New("máy chủ SMTP không hỗ trợ STARTTLS: không gửi mật khẩu qua kết nối không mã hóa")
	}
	if err := c.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("STARTTLS: %w", err)
	}
	if err := c.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
		return fmt.Errorf("đăng nhập SMTP: %w", err)
	}
	from := s.From
	if from == "" {
		from = s.User
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	msg := "From: SCS-System <" + from + ">\r\n" +
		"To: <" + to + ">\r\n" +
		"Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

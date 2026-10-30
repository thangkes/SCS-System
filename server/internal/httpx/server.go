// Package httpx gom phần HTTP dùng chung: chạy server, TLS, middleware, health check.
package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// Server là một HTTP server có tên (ingest, enroll, admin) và tự tắt êm khi ctx bị hủy.
type Server struct {
	Name    string
	Addr    string
	Handler http.Handler
	TLS     *tls.Config // nil = HTTP không mã hóa (chỉ cho dev)
	Log     *slog.Logger
	Allow   []netip.Prefix // rỗng = nhận mọi IP; có giá trị = chỉ nhận kết nối từ các dải này

	MaxConns      int           // số kết nối đang mở tối đa (0 = không giới hạn)
	MaxConnsPerIP int           // số kết nối đang mở tối đa của một IP nguồn (0 = không giới hạn)
	IdleTimeout   time.Duration // giữ kết nối keep-alive nhàn rỗi bao lâu (0 = 120 giây)
	// DisableHTTP2: chỉ dùng HTTP/1.1. HTTP/2 cho mỗi kết nối chạy 250 request song song,
	// nhân giới hạn theo kết nối lên 250 lần; agent không cần HTTP/2.
	DisableHTTP2 bool
}

const shutdownTimeout = 10 * time.Second

// Run lắng nghe cho tới khi ctx bị hủy rồi chờ các request đang chạy xong.
// Trả về nil khi tắt bình thường, lỗi nếu không mở được cổng hoặc server chết.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("%s: không mở được %s: %w", s.Name, s.Addr, err)
	}
	if len(s.Allow) > 0 {
		ln = newAllowListener(ln, s.Allow, s.Log)
	}
	if s.MaxConns > 0 || s.MaxConnsPerIP > 0 {
		ln = newConnLimitListener(ln, s.MaxConns, s.MaxConnsPerIP, s.Log)
	}
	idle := s.IdleTimeout
	if idle == 0 {
		idle = 120 * time.Second
	}
	srv := &http.Server{
		Handler:           s.Handler,
		TLSConfig:         s.TLS,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       idle,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          ServerErrorLog(s.Log), // gộp lỗi bắt tay TLS (bot quét cổng)
	}
	if s.DisableHTTP2 {
		srv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){} // map rỗng = tắt HTTP/2
	}

	errc := make(chan error, 1)
	go func() {
		if s.TLS != nil {
			errc <- srv.ServeTLS(ln, "", "")
		} else {
			errc <- srv.Serve(ln)
		}
	}()
	s.Log.Info("đang lắng nghe", "addr", ln.Addr().String(), "tls", s.TLS != nil)

	select {
	case err := <-errc:
		return fmt.Errorf("%s: %w", s.Name, err)
	case <-ctx.Done():
	}
	s.Log.Info("đang tắt, chờ request dở dang")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("%s: tắt không êm: %w", s.Name, err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	return nil
}

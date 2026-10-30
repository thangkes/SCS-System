// Package client gọi server: HTTPS chỉ tin CA của server (đóng sẵn trong gói), gửi chứng chỉ
// client khi đã được duyệt, ký mọi gói tin bằng khóa của máy.
//
// Mỗi lần gọi xong thì đóng kết nối (không giữ kết nối nhàn rỗi trên server) và lưu phiên
// TLS để lần sau bắt tay nhanh, tốn ít băng thông hơn.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/identity"
	"github.com/thangkes/SCS-System/common/protocol"
)

// Client gọi các API của server.
type Client struct {
	base    string
	tls     *tls.Config
	http    *http.Client
	key     *identity.Key
	keyDir  string
	version string
	offset  atomic.Int64 // giờ server - giờ máy (ns), học từ header Date
}

// New tạo client. caFile là CA của server; không đọc được thì không gọi server được
// (agent không bao giờ bỏ qua kiểm chứng chỉ server).
func New(server, caFile, keyDir string, key *identity.Key, version string) (*Client, error) {
	pemBytes, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("đọc CA của server (%s): %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("%s không có chứng chỉ PEM hợp lệ", caFile)
	}
	c := &Client{base: strings.TrimRight(server, "/"), key: key, keyDir: keyDir, version: version}
	c.tls = &tls.Config{
		MinVersion:           tls.VersionTLS13,
		RootCAs:              pool,
		GetClientCertificate: c.clientCert,
	}
	c.ResetSessions()
	return c, nil
}

// ResetSessions bỏ các phiên TLS đã lưu. Gọi sau khi nhận chứng chỉ mới: phiên cũ lập lúc
// chưa có chứng chỉ, nối lại phiên đó thì server thấy kết nối không có chứng chỉ client.
func (c *Client) ResetSessions() {
	cfg := c.tls.Clone()
	cfg.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	c.http = &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:       cfg,
			DisableKeepAlives:     true,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ForceAttemptHTTP2:     false,
			Proxy:                 http.ProxyFromEnvironment,
		},
	}
}

// clientCert gửi chứng chỉ máy khi đã có (sau khi được duyệt); chưa có thì không gửi gì.
func (c *Client) clientCert(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(identity.CertPath(c.keyDir), identity.KeyPath(c.keyDir))
	if err != nil {
		return &tls.Certificate{}, nil
	}
	return &cert, nil
}

// Now là giờ đã bù theo server (dùng cho trường ts của gói tin).
func (c *Client) Now() time.Time { return time.Now().Add(time.Duration(c.offset.Load())) }

// Offset: máy đang lệch giờ so với server bao nhiêu.
func (c *Client) Offset() time.Duration { return time.Duration(c.offset.Load()) }

// HTTPError: server trả mã lỗi. RetryAfter lấy từ header Retry-After (nếu có).
type HTTPError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("server trả %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("server trả %d", e.Status)
}

// Send ký gói tin rồi POST tới path; trả lời 200 thì giải JSON vào out.
func (c *Client) Send(ctx context.Context, path, typ, deviceID, fingerprint string, body, out any) error {
	env, err := protocol.NewEnvelope(typ, deviceID, fingerprint, c.version, body)
	if err != nil {
		return err
	}
	env.TS = c.Now().UTC().Truncate(time.Millisecond)
	signed, err := protocol.Sign(c.key.Private, env)
	if err != nil {
		return err
	}
	b, err := json.Marshal(signed)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "scs-agent/"+c.version)
	sent := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	c.learnClock(resp.Header.Get("Date"), sent, time.Now())

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		herr := &HTTPError{Status: resp.StatusCode, Message: e.Error}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			herr.RetryAfter = time.Duration(s) * time.Second
		}
		return herr
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("trả lời của server sai định dạng: %w", err)
		}
	}
	return nil
}

// learnClock: header Date của server (độ phân giải 1 giây) so với giờ máy ở giữa lúc gửi và
// nhận. Chỉ chỉnh khi lệch quá 2 giây để không dao động vì làm tròn.
func (c *Client) learnClock(date string, sent, recv time.Time) {
	t, err := http.ParseTime(date)
	if err != nil {
		return
	}
	mid := sent.Add(recv.Sub(sent) / 2)
	off := t.Add(500 * time.Millisecond).Sub(mid) // Date bị cắt về đầu giây
	if d := off - time.Duration(c.offset.Load()); d > 2*time.Second || d < -2*time.Second {
		c.offset.Store(int64(off))
	}
}

// IsRetryable: lỗi mạng, server bận (429/503) hoặc lỗi phía server: thử lại sau.
func IsRetryable(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests || he.Status >= 500
	}
	return err != nil
}

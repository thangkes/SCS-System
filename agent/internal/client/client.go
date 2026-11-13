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
	"sync"
	"sync/atomic"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/identity"
	"github.com/thangkes/SCS-System/common/protocol"
)

// Client gọi các API của server.
//
// Có thể có nhiều địa chỉ server (LAN, IP WAN, tên miền): địa chỉ khai lúc cài đứng đầu, sau đó
// là danh sách server gửi xuống. Không kết nối được địa chỉ đang dùng thì thử địa chỉ kế tiếp
// ngay trong lần gọi đó, được thì dùng tiếp địa chỉ ấy. Mọi địa chỉ đều phải có chứng chỉ do
// CA của server ký nên danh sách không mở thêm đường cho kẻ giả mạo.
type Client struct {
	mu      sync.Mutex
	bases   []string
	cur     int
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
	c := &Client{bases: []string{strings.TrimRight(server, "/")}, key: key, keyDir: keyDir, version: version}
	c.tls = &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Không so IP / tên miền trong chứng chỉ server: agent gọi server bằng địa chỉ nào cũng được
		// (IP LAN, VLAN khác, IP WAN qua NAT, tên miền) mà không phải cấp lại chứng chỉ khi đổi IP.
		// Thay vào đó verifyServer bắt buộc chứng chỉ do CA riêng của server ký và dùng cho server:
		// CA này chỉ cấp chứng chỉ server cho scs-server, chứng chỉ máy agent chỉ dùng cho client.
		InsecureSkipVerify:   true,
		VerifyConnection:     verifyServer(pool),
		GetClientCertificate: c.clientCert,
	}
	c.ResetSessions()
	return c, nil
}

// verifyServer: chứng chỉ server phải chuỗi về CA của server và có mục đích ServerAuth.
// Chạy cả khi nối lại phiên TLS cũ.
func verifyServer(roots *x509.CertPool) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("server không gửi chứng chỉ")
		}
		inter := x509.NewCertPool()
		for _, c := range cs.PeerCertificates[1:] {
			inter.AddCert(c)
		}
		_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
			Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if err != nil {
			return fmt.Errorf("chứng chỉ server không do CA của server cấp: %w", err)
		}
		return nil
	}
}

// ResetSessions bỏ các phiên TLS đã lưu. Gọi sau khi nhận chứng chỉ mới: phiên cũ lập lúc
// chưa có chứng chỉ, nối lại phiên đó thì server thấy kết nối không có chứng chỉ client.
func (c *Client) ResetSessions() {
	cfg := c.tls.Clone()
	cfg.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	hc := &http.Client{
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
	c.mu.Lock()
	c.http = hc
	c.mu.Unlock()
}

// SetServers đặt danh sách địa chỉ server gửi xuống (địa chỉ lúc cài luôn giữ ở đầu).
// Địa chỉ đang dùng vẫn được dùng tiếp nếu còn trong danh sách mới.
func (c *Client) SetServers(list []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur := c.bases[c.cur]
	c.bases = protocol.CleanServers(append([]string{c.bases[0]}, list...))
	c.cur = 0
	for i, b := range c.bases {
		if b == cur {
			c.cur = i
		}
	}
}

// Prefer: bắt đầu từ địa chỉ u (địa chỉ gọi được lần cuối, lưu trong state) nếu có trong danh sách.
// Nhờ vậy sau khi server đổi mạng và địa chỉ lúc cài không còn dùng được, agent khởi động lại
// không phải chờ hết thời gian chờ của địa chỉ lúc cài rồi mới chuyển.
func (c *Client) Prefer(u string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, b := range c.bases {
		if b == u {
			c.cur = i
		}
	}
}

// Servers: danh sách địa chỉ đang dùng; Current: địa chỉ đang gọi.
func (c *Client) Servers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bases...)
}

func (c *Client) Current() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bases[c.cur]
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
	c.mu.Lock()
	bases, start, hc := append([]string(nil), c.bases...), c.cur, c.http
	c.mu.Unlock()
	var resp *http.Response
	var sent time.Time
	for i := range bases {
		idx := (start + i) % len(bases)
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, bases[idx]+path, bytes.NewReader(b))
		if rerr != nil {
			return rerr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "scs-agent/"+c.version)
		sent = time.Now()
		resp, err = hc.Do(req)
		if err == nil {
			c.mu.Lock()
			if idx < len(c.bases) && c.bases[idx] == bases[idx] {
				c.cur = idx
			}
			c.mu.Unlock()
			break
		}
		if ctx.Err() != nil {
			return err
		}
	}
	if err != nil {
		return err // không địa chỉ nào kết nối được
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

// Ping gọi GET /healthz (không cần chứng chỉ máy) để kiểm kết nối và chứng chỉ server, thử lần
// lượt các địa chỉ như Send. Trả địa chỉ đã kết nối được.
func (c *Client) Ping(ctx context.Context) (string, error) {
	c.mu.Lock()
	bases, hc := append([]string(nil), c.bases...), c.http
	c.mu.Unlock()
	var errs []error
	for _, base := range bases {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "scs-agent/"+c.version)
		resp, err := hc.Do(req)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", base, err))
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"ok"`) {
			errs = append(errs, fmt.Errorf("%s: server trả %d", base, resp.StatusCode))
			continue
		}
		return base, nil
	}
	return "", errors.Join(errs...)
}

// Download GET path (cần chứng chỉ máy) và trả nội dung, tối đa max byte. Thử lần lượt các địa
// chỉ như Send; server trả lỗi thì dừng. Bản tải lớn nên không dùng giới hạn 60 giây của Send.
func (c *Client) Download(ctx context.Context, path string, max int64) ([]byte, error) {
	c.mu.Lock()
	bases, start, hc := append([]string(nil), c.bases...), c.cur, c.http
	c.mu.Unlock()
	dl := &http.Client{Transport: hc.Transport, Timeout: 15 * time.Minute}
	var lastErr error
	for i := range bases {
		base := bases[(start+i)%len(bases)]
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "scs-agent/"+c.version)
		resp, err := dl.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, err
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			var e struct {
				Error string `json:"error"`
			}
			json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
			return nil, &HTTPError{Status: resp.StatusCode, Message: e.Error}
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > max {
			return nil, fmt.Errorf("file tải về lớn hơn %d byte", max)
		}
		return data, nil
	}
	return nil, lastErr
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

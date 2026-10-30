package httpx

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// ClientAuth quy định server có đòi chứng chỉ của agent hay không.
type ClientAuth int

const (
	NoClientCert       ClientAuth = iota // admin
	OptionalClientCert                   // ingest: máy mới chưa có chứng chỉ được vào /v1/enroll, các route khác tự đòi
	RequireClientCert                    // bắt buộc mTLS ngay lúc bắt tay TLS
)

// ServerTLS tạo cấu hình TLS 1.3. certFile rỗng nghĩa là không dùng TLS (trả nil).
// clientCAFile rỗng thì không kiểm chứng chỉ client (chỉ được phép khi dev,
// config.Load đã chặn trường hợp này ở prod).
//
// Chứng chỉ server được tự nạp lại khi file thay đổi (step-ca gia hạn chứng chỉ ngắn hạn
// mà không cần khởi động lại server). Client CA chỉ đọc lúc khởi động: đổi CA thì restart.
func ServerTLS(certFile, keyFile, clientCAFile string, mode ClientAuth, log *slog.Logger) (*tls.Config, error) {
	if certFile == "" {
		return nil, nil
	}
	r, err := newCertReloader(certFile, keyFile, log)
	if err != nil {
		return nil, fmt.Errorf("đọc chứng chỉ server: %w", err)
	}
	log.Info("chứng chỉ TLS server", "file", certFile, "het_han", r.cert.Leaf.NotAfter.Local().Format(time.DateTime))
	// Không đặt Certificates: crypto/tls chỉ gọi GetCertificate khi Certificates rỗng
	// hoặc client gửi SNI; agent gọi bằng IP thì không có SNI.
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: r.get,
	}
	if mode == NoClientCert || clientCAFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("đọc client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("client CA không có chứng chỉ PEM hợp lệ")
	}
	cfg.ClientCAs = pool
	if mode == RequireClientCert {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
	}
	return cfg, nil
}

// CertReloadEvery: kiểm file chứng chỉ có thay đổi không tối đa mỗi khoảng này
// (kiểm lúc có kết nối mới). Test đặt 0.
var CertReloadEvery = time.Minute

// Còn ít hơn ngần này thì cảnh báo trong log mỗi giờ (gia hạn đang hỏng?).
const certExpireWarn = 7 * 24 * time.Hour

// certReloader giữ chứng chỉ server và nạp lại khi file đổi (so thời gian sửa + kích thước).
// File mới lỗi (đang ghi dở, cert và key không khớp) thì giữ chứng chỉ cũ và ghi log.
type certReloader struct {
	certFile, keyFile string
	log               *slog.Logger

	mu      sync.Mutex
	cert    *tls.Certificate
	stamp   string
	checked time.Time
	warned  time.Time
}

func newCertReloader(certFile, keyFile string, log *slog.Logger) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, log: log, checked: time.Now()}
	stamp, err := r.fileStamp()
	if err != nil {
		return nil, err
	}
	if err := r.load(stamp); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) fileStamp() (string, error) {
	stamp := ""
	for _, f := range []string{r.certFile, r.keyFile} {
		st, err := os.Stat(f)
		if err != nil {
			return "", err
		}
		stamp += fmt.Sprintf("%d:%d;", st.ModTime().UnixNano(), st.Size())
	}
	return stamp, nil
}

func (r *certReloader) load(stamp string) error {
	c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	if c.Leaf == nil {
		if c.Leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return err
		}
	}
	r.cert, r.stamp = &c, stamp
	return nil
}

func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if now.Sub(r.checked) < CertReloadEvery {
		return r.cert, nil
	}
	r.checked = now
	switch stamp, err := r.fileStamp(); {
	case err != nil:
		r.log.Warn("không đọc được file chứng chỉ TLS, vẫn dùng chứng chỉ đang có", "err", err)
	case stamp != r.stamp:
		if err := r.load(stamp); err != nil {
			r.stamp = stamp // ghi log một lần cho mỗi lần file đổi, không phải mỗi phút
			r.log.Error("chứng chỉ TLS mới bị lỗi, vẫn dùng chứng chỉ cũ", "err", err,
				"chung_chi_cu_het_han", r.cert.Leaf.NotAfter.Local().Format(time.DateTime))
		} else {
			r.log.Info("đã nạp chứng chỉ TLS mới (gia hạn)", "het_han", r.cert.Leaf.NotAfter.Local().Format(time.DateTime))
		}
	}
	if left := time.Until(r.cert.Leaf.NotAfter); left < certExpireWarn && now.Sub(r.warned) >= time.Hour {
		r.warned = now
		r.log.Warn("chứng chỉ TLS server sắp hết hạn hoặc đã hết hạn, kiểm tra việc gia hạn (step-ca)",
			"het_han", r.cert.Leaf.NotAfter.Local().Format(time.DateTime), "con_lai", left.Round(time.Minute).String())
	}
	return r.cert, nil
}

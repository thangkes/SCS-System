// Package identity giữ danh tính của máy: khóa riêng Ed25519 (sinh một lần, không rời máy)
// và trạng thái đăng ký với server.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const keyFile = "key.pem"

// Key là khóa riêng của máy.
type Key struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
	Path    string
}

// ID là mã ngắn của khóa (16 ký tự hex đầu của SHA-256 public key), để kỹ thuật viên
// đối chiếu với yêu cầu đang chờ duyệt trên web.
func (k *Key) ID() string {
	sum := sha256.Sum256(k.Public)
	return hex.EncodeToString(sum[:8])
}

// ErrNoKey: máy chưa có khóa (chưa chạy lần nào).
var ErrNoKey = errors.New("máy chưa có khóa")

// LoadOrCreateKey đọc khóa trong dir; chưa có thì sinh mới và lưu với quyền 600.
// Khóa có quyền rộng hơn 600 thì từ chối: có thể đã bị người khác đọc, phải xử lý tay.
func LoadOrCreateKey(dir string) (*Key, error) {
	k, err := LoadKey(dir)
	if errors.Is(err, ErrNoKey) {
		return createKey(dir, filepath.Join(dir, keyFile))
	}
	return k, err
}

// LoadKey chỉ đọc khóa, không tạo mới (lệnh xem thông tin).
func LoadKey(dir string) (*Key, error) {
	path := filepath.Join(dir, keyFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, fmt.Errorf("đọc khóa máy: %w", err)
	}
	if err := checkPrivate(path); err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s không phải khóa PEM hợp lệ", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s không phải khóa Ed25519", path)
	}
	return &Key{Private: priv, Public: priv.Public().(ed25519.PublicKey), Path: path}, nil
}

func createKey(dir, path string) (*Key, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("tạo thư mục khóa: %w", err)
	}
	if err := writeFileAtomic(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		return nil, fmt.Errorf("lưu khóa máy: %w", err)
	}
	return &Key{Private: priv, Public: pub, Path: path}, nil
}

// checkPrivate: file bí mật chỉ chủ sở hữu được đọc. Windows không có quyền kiểu Unix (chỉ dev).
func checkPrivate(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if p := st.Mode().Perm(); p&0o077 != 0 {
		return fmt.Errorf("%s đang quyền %o, phải là 600: khóa có thể đã bị đọc; kiểm tra rồi chạy chmod 600 %s", path, p, path)
	}
	return nil
}

// writeFileAtomic ghi file quyền 600 qua file tạm rồi đổi tên: mất điện giữa chừng
// cũng không để lại file ghi dở.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // còn sót khi lỗi; đổi tên xong thì không còn gì để xóa
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ---------------------------------------------------------------- trạng thái đăng ký

// Trạng thái đăng ký của máy.
const (
	StatusNew      = "new"      // chưa gửi đăng ký
	StatusPending  = "pending"  // đã gửi, chờ admin duyệt
	StatusApproved = "approved" // đã có chứng chỉ, gửi dữ liệu bình thường
	StatusRejected = "rejected" // admin từ chối hoặc thu hồi: dừng gửi
)

const stateFile = "state.json"

// State là trạng thái lưu ở <state dir>/state.json.
type State struct {
	Status         string    `json:"status"`
	DeviceID       string    `json:"device_id,omitempty"`
	RequestedAt    time.Time `json:"requested_at,omitzero"`
	ApprovedAt     time.Time `json:"approved_at,omitzero"`
	RuleSetVersion string    `json:"rule_set_version,omitempty"`
	// Servers: địa chỉ server gửi xuống (LAN, IP WAN, tên miền), thử lần lượt khi mất kết nối.
	Servers []string `json:"servers,omitempty"`
	// LastServer: địa chỉ gọi server thành công lần cuối; khởi động lại thì thử địa chỉ này trước.
	LastServer string `json:"last_server,omitempty"`
	// Cấu hình server gửi xuống (đặt trên web). WatchServices nil = chưa nhận, dùng agent.env.
	ConfigVersion string   `json:"config_version,omitempty"`
	WatchServices []string `json:"watch_services,omitempty"`
}

// LoadState đọc trạng thái; chưa có file thì là máy mới.
func LoadState(dir string) (State, error) {
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return State{Status: StatusNew}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("%s hỏng: %w", stateFile, err)
	}
	switch s.Status {
	case StatusNew, StatusPending, StatusApproved, StatusRejected:
	default:
		return State{}, fmt.Errorf("%s: trạng thái lạ %q", stateFile, s.Status)
	}
	return s, nil
}

// SaveState ghi trạng thái (ghi tạm rồi đổi tên).
func SaveState(dir string, s State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, stateFile), append(b, '\n'))
}

// ---------------------------------------------------------------- chứng chỉ client

const certFile = "cert.pem"

// CertPath: chứng chỉ client server cấp sau khi admin duyệt (đi cùng key.pem).
func CertPath(dir string) string { return filepath.Join(dir, certFile) }

// KeyPath: khóa riêng của máy.
func KeyPath(dir string) string { return filepath.Join(dir, keyFile) }

// SaveCert lưu chứng chỉ client (ghi tạm rồi đổi tên, quyền 600).
func SaveCert(dir string, certPEM []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(CertPath(dir), certPEM)
}

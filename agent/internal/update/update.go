// Package update: agent tự cập nhật từ server.
//
// Bản cài lần đầu là bản "khung" cố định, root sở hữu (/opt/scs-agent/scs-agent), dịch vụ không sửa
// được. Bản mới server phát hành được tải về thư mục trạng thái (<state>/update/) và chỉ được chạy
// khi đúng chữ ký của khóa phát hành (release.pub, root sở hữu, đóng trong gói cài):
//
//   - Apply: tải bản server chỉ định, kiểm SHA-256 + chữ ký, ghi lại làm bản cần chạy.
//   - Launch: lúc khởi động, bản khung kiểm lại chữ ký rồi chuyển sang chạy bản đó (exec).
//     Bản mới chết MaxTries lần (trong cùng một lần bật máy) mà chưa gửi được heartbeat thì bị
//     đánh dấu hỏng, bản khung chạy tiếp và không tải lại bản đó trong BadFor (24 giờ).
//     Máy tắt / khởi động lại giữa chừng (đổi boot_id) không bị tính là bản mới chết; mất mạng
//     cũng không, vì bản mới vẫn chạy và tự thử gọi server lại.
//   - MarkGood: bản mới gửi heartbeat thành công thì được ghi nhận là chạy tốt.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/rootfile"
	"github.com/thangkes/SCS-System/common/protocol"
)

// ChildEnv: biến môi trường đánh dấu tiến trình là bản đã được bản khung chuyển sang (không
// chuyển tiếp lần nữa).
const ChildEnv = "SCS_AGENT_CHILD"

// MaxTries: số lần bản mới chết (chưa gửi được heartbeat) trước khi coi là hỏng.
const MaxTries = 3

// BadFor: bản bị coi là hỏng thì bấy lâu sau mới được tải và thử lại (lỡ đánh dấu oan thì tự hết).
var BadFor = 24 * time.Hour

// Thay được trong test.
var (
	now    = time.Now
	bootID = readBootID
)

// readBootID: mã của lần bật máy hiện tại (Linux); đổi sau mỗi lần khởi động lại. "" nếu không có.
func readBootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Current: bản agent cần chạy, lưu ở <state>/update/current.json.
type Current struct {
	protocol.UpdateInfo
	// Tries: số lần bản này đã chết khi chưa được ghi nhận chạy tốt.
	Tries int  `json:"tries"`
	Good  bool `json:"good"`
	// GoodAt: lúc bản này lần đầu gửi heartbeat tốt (để biết đã chạy ổn bao lâu trước khi làm bản khung).
	GoodAt time.Time `json:"good_at,omitempty"`
	// Started + BootID: đã chuyển sang bản này trong lần bật máy nào. Lần khởi động sau cùng
	// boot_id nghĩa là tiến trình trước đã chết (tính một lần); khác boot_id là máy đã khởi
	// động lại (không tính).
	Started bool   `json:"started,omitempty"`
	BootID  string `json:"boot_id,omitempty"`
	// Bad: các bản đã hỏng, không tải lại cho tới BadAt + BadFor.
	Bad   []string             `json:"bad,omitempty"`
	BadAt map[string]time.Time `json:"bad_at,omitempty"`
}

// markBad ghi bản v là hỏng từ lúc này.
func (c *Current) markBad(v string) {
	if !slices.Contains(c.Bad, v) {
		c.Bad = append(c.Bad, v)
	}
	if c.BadAt == nil {
		c.BadAt = map[string]time.Time{}
	}
	c.BadAt[v] = now()
}

// pruneBad bỏ các dấu hỏng đã quá BadFor. Dấu cũ chưa có thời điểm (bản agent trước) tính từ lúc này.
func (c *Current) pruneBad() {
	keep := c.Bad[:0]
	for _, v := range c.Bad {
		at, ok := c.BadAt[v]
		if !ok {
			if c.BadAt == nil {
				c.BadAt = map[string]time.Time{}
			}
			at = now()
			c.BadAt[v] = at
		}
		if now().Sub(at) < BadFor {
			keep = append(keep, v)
		} else {
			delete(c.BadAt, v)
		}
	}
	c.Bad = keep
	if len(c.Bad) == 0 {
		c.Bad, c.BadAt = nil, nil
	}
}

// Downloader tải một đường dẫn trên server (client.Client.Download).
type Downloader interface {
	Download(ctx context.Context, path string, max int64) ([]byte, error)
}

// execBinary chuyển sang chạy bản cập nhật (test thay được).
var execBinary = execBinaryOS

// ErrBadRelease: bản này đã từng khởi động lỗi trên máy, không tải lại.
var ErrBadRelease = errors.New("bản này đã khởi động lỗi trên máy này trước đó")

func dir(stateDir string) string { return filepath.Join(stateDir, "update") }

// binPath: file của một bản trong thư mục cập nhật.
func binPath(stateDir, version string) string {
	return filepath.Join(dir(stateDir), "scs-agent-"+version)
}

// Load đọc bản cần chạy; chưa có thì trả Current rỗng.
func Load(stateDir string) (Current, error) {
	var c Current
	b, err := os.ReadFile(filepath.Join(dir(stateDir), "current.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func save(stateDir string, c Current) error {
	if err := os.MkdirAll(dir(stateDir), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir(stateDir), "current.json"), b, 0o600)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadPub đọc khóa công khai phát hành (PEM PKIX Ed25519).
func LoadPub(path string) (ed25519.PublicKey, error) {
	b, err := rootfile.Read(path) // ở /opt thì phải thuộc root (xem rootfile)
	if err != nil {
		return nil, fmt.Errorf("đọc khóa phát hành %s: %w", path, err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("%s không phải khóa công khai PEM", path)
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%s: khóa phát hành phải là Ed25519", path)
	}
	return pub, nil
}

// Apply tải bản info từ server, kiểm SHA-256 và chữ ký, ghi lại làm bản cần chạy ở lần khởi
// động tới. Xóa các bản cũ khác trong thư mục cập nhật.
func Apply(ctx context.Context, dl Downloader, info protocol.UpdateInfo, stateDir, pubPath string) error {
	cur, err := Load(stateDir)
	if err != nil {
		return err
	}
	cur.pruneBad()
	if slices.Contains(cur.Bad, info.Version) {
		return ErrBadRelease
	}
	if info.Arch != runtime.GOARCH {
		return fmt.Errorf("server gửi bản %s, máy này là %s", info.Arch, runtime.GOARCH)
	}
	if !protocol.ValidVersion(info.Version) || info.Size <= 0 || info.Size > protocol.MaxAgentBinary {
		return errors.New("thông tin bản cập nhật không hợp lệ")
	}
	pub, err := LoadPub(pubPath)
	if err != nil {
		return err
	}
	data, err := dl.Download(ctx, "/v1/updates/"+info.Version+"?arch="+info.Arch, info.Size)
	if err != nil {
		return fmt.Errorf("tải bản %s: %w", info.Version, err)
	}
	if err := protocol.VerifyRelease(pub, info, data); err != nil {
		return fmt.Errorf("bản %s: %w", info.Version, err)
	}
	if err := os.MkdirAll(dir(stateDir), 0o700); err != nil {
		return err
	}
	if err := writeAtomic(binPath(stateDir, info.Version), data, 0o700); err != nil {
		return err
	}
	if err := save(stateDir, Current{UpdateInfo: info, Bad: cur.Bad, BadAt: cur.BadAt}); err != nil {
		return err
	}
	// Dọn bản cũ: chỉ giữ bản vừa tải.
	if entries, err := os.ReadDir(dir(stateDir)); err == nil {
		keep := filepath.Base(binPath(stateDir, info.Version))
		for _, e := range entries {
			if n := e.Name(); n != keep && n != "current.json" {
				os.Remove(filepath.Join(dir(stateDir), n))
			}
		}
	}
	return nil
}

// MarkGood: bản đang chạy (version) đã gửi heartbeat thành công.
func MarkGood(stateDir, version string) error {
	cur, err := Load(stateDir)
	if err != nil || cur.Version != version || (cur.Good && !cur.GoodAt.IsZero()) {
		return err
	}
	// Bản đã được ghi nhận tốt từ trước (bởi bản agent cũ, chưa có GoodAt) thì tính giờ từ bây giờ.
	cur.Good, cur.Tries, cur.GoodAt = true, 0, now().UTC()
	return save(stateDir, cur)
}

// Launch: gọi đầu lệnh run. Có bản cập nhật hợp lệ (khác bản đang chạy) thì chuyển sang chạy bản
// đó và không quay lại; không có, chữ ký sai hoặc bản đã hỏng thì trả về để chạy tiếp bản này.
func Launch(log *slog.Logger, stateDir, pubPath, selfVersion string) {
	if os.Getenv(ChildEnv) != "" {
		return
	}
	cur, err := Load(stateDir)
	if err != nil {
		log.Warn("không đọc được thông tin bản cập nhật, chạy bản đang cài", "err", err)
		return
	}
	if cur.Version == "" || cur.Version == selfVersion {
		return
	}
	fail := func(reason string, err error) {
		log.Error("không chạy bản cập nhật, chạy bản đang cài", "version", cur.Version, "ly_do", reason, "err", err)
		cur.markBad(cur.Version)
		cur.UpdateInfo, cur.Tries, cur.Good, cur.Started, cur.BootID = protocol.UpdateInfo{}, 0, false, false, ""
		save(stateDir, cur)
	}
	if !cur.Good {
		boot := bootID()
		switch {
		case !cur.Started:
			// lần đầu chuyển sang bản này
		case boot != "" && boot != cur.BootID:
			log.Info("máy đã khởi động lại khi bản mới chưa gọi được server: không tính là lỗi", "version", cur.Version)
		default:
			cur.Tries++ // cùng lần bật máy mà phải khởi động lại: bản trước đã chết
		}
		if cur.Tries >= MaxTries {
			fail(fmt.Sprintf("đã chết %d lần mà chưa gửi được heartbeat", cur.Tries), nil)
			return
		}
		cur.Started, cur.BootID = true, boot
		if err := save(stateDir, cur); err != nil {
			log.Warn("không ghi được số lần thử bản cập nhật, chạy bản đang cài", "err", err)
			return
		}
	}
	pub, err := LoadPub(pubPath)
	if err != nil {
		log.Warn("không có khóa phát hành, chạy bản đang cài", "err", err)
		return
	}
	path := binPath(stateDir, cur.Version)
	data, err := os.ReadFile(path)
	if err == nil {
		err = protocol.VerifyRelease(pub, cur.UpdateInfo, data)
	}
	if err != nil {
		fail("file bản cập nhật thiếu hoặc sai chữ ký", err)
		return
	}
	StampOK(stateDir, selfVersion) // bản khung đã khởi động, kiểm được chữ ký và chuyển được: nó chạy tốt
	log.Info("chuyển sang chạy bản cập nhật", "tu", selfVersion, "sang", cur.Version, "lan_thu", cur.Tries)
	if err := execBinary(path); err != nil {
		log.Error("không chạy được bản cập nhật, chạy bản đang cài", "err", err)
	}
}

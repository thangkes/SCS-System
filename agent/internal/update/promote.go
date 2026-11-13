package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

// Thay bản khung từ server ("promote").
//
// Bản khung (/opt/scs-agent/scs-agent) thuộc root để tiến trình agent (user scs-agent) không tự
// sửa được: kẻ chiếm được tiến trình agent không cài được bản của riêng nó. Vì thế việc thay bản
// khung do một tác vụ chạy bằng ROOT làm (scs-agent-promote.timer → "scs-agent promote"), và tác
// vụ đó không tin gì trong thư mục trạng thái (nơi agent ghi được) ngoài thứ kiểm được bằng chữ ký:
//
//  1. Bản mới server phát hành chạy như bình thường (bản khung kiểm chữ ký rồi chuyển sang).
//  2. Khi bản đó đã gửi heartbeat tốt trên chính máy này được PromoteAfter, tác vụ root đọc file
//     bản đó, kiểm SHA-256 + chữ ký bằng release.pub (root sở hữu), chạy thử "version", rồi chép
//     vào /opt thay bản khung. Bản khung cũ giữ lại ở scs-agent.prev.
//  3. Lần khởi động sau, bản khung mới ghi dấu "đã khởi động"; khi nó gọi được server (hoặc chuyển
//     được sang bản khác) thì ghi dấu "chạy tốt". Có dấu khởi động mà quá RollbackAfter chưa có dấu
//     chạy tốt thì tác vụ root trả lại bản khung cũ và không thay bằng bản đó nữa.
//
// Kẻ chiếm được tiến trình agent chỉ làm được hai việc qua cơ chế này: ép thay bản khung bằng một
// bản KHÁC CŨNG CÓ CHỮ KÝ của server (ví dụ bản cũ), hoặc ép quay về bản khung trước. Cả hai đều
// là bản server đã phát hành; nó không cài được bản tự chế.

const (
	bootStateFile = "bootstrap.json"       // trong thư mục cài (root sở hữu)
	startStamp    = "bootstrap-start.json" // trong thư mục trạng thái (agent ghi)
	okStamp       = "bootstrap-ok"
	binName       = "scs-agent"
)

// Thời gian chờ (test đổi được).
var (
	PromoteAfter  = time.Hour        // bản mới phải chạy tốt bấy lâu mới được làm bản khung
	RollbackAfter = 30 * time.Minute // bản khung mới khởi động mà bấy lâu chưa chạy tốt thì quay về
)

// Các phép phụ thuộc hệ điều hành (test thay được).
var (
	ownedByRoot  = ownedByRootOS  // file / thư mục thuộc root và người khác không ghi được
	readRegular  = readRegularOS  // đọc file thường, không đi theo symlink
	probeVersion = probeVersionOS // chạy "<binary> version"
)

// BootState: tình trạng bản khung, do tác vụ root ghi trong thư mục cài.
type BootState struct {
	Version    string    `json:"version,omitempty"`     // bản khung vừa được thay vào
	Prev       string    `json:"prev,omitempty"`        // phiên bản bản khung cũ (đang giữ ở scs-agent.prev)
	PromotedAt time.Time `json:"promoted_at,omitempty"` // lúc thay
	Confirmed  bool      `json:"confirmed,omitempty"`   // bản khung mới đã chạy tốt
	NoPromote  []string  `json:"no_promote,omitempty"`  // các bản đã phải quay về, không thay lại
}

type stamp struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// StampStart: bản khung (không phải bản được chuyển sang) ghi dấu vừa khởi động.
func StampStart(stateDir, version string) {
	if os.Getenv(ChildEnv) != "" {
		return
	}
	b, _ := json.Marshal(stamp{Version: version, At: now().UTC()})
	_ = os.MkdirAll(stateDir, 0o700)
	_ = writeAtomic(filepath.Join(stateDir, startStamp), b, 0o600)
}

// StampOK: bản khung ghi dấu nó chạy tốt (gọi được server, hoặc chuyển được sang bản khác).
func StampOK(stateDir, version string) {
	if os.Getenv(ChildEnv) != "" {
		return
	}
	_ = writeAtomic(filepath.Join(stateDir, okStamp), []byte(version), 0o600)
}

func loadBoot(installDir string) (BootState, error) {
	var bs BootState
	b, err := os.ReadFile(filepath.Join(installDir, bootStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return bs, nil
	}
	if err != nil {
		return bs, err
	}
	return bs, json.Unmarshal(b, &bs)
}

func saveBoot(installDir string, bs BootState) error {
	b, err := json.MarshalIndent(bs, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(installDir, bootStateFile), b, 0o644)
}

// Promote: một lượt của tác vụ root. Trả câu mô tả việc đã làm ("" = không có gì để làm).
func Promote(stateDir, installDir, pubPath string) (string, error) {
	bin := filepath.Join(installDir, binName)
	for _, p := range []string{installDir, bin, pubPath} {
		if err := ownedByRoot(p); err != nil {
			return "", fmt.Errorf("không an toàn để thay bản khung: %w", err)
		}
	}
	bs, err := loadBoot(installDir)
	if err != nil {
		return "", fmt.Errorf("đọc %s: %w", bootStateFile, err)
	}

	// 1. Bản khung vừa thay: đã chạy tốt chưa, hay phải quay về?
	if !bs.PromotedAt.IsZero() && !bs.Confirmed {
		if ok, _ := readRegular(filepath.Join(stateDir, okStamp), 256); strings.TrimSpace(string(ok)) == bs.Version {
			bs.Confirmed = true
			if err := saveBoot(installDir, bs); err != nil {
				return "", err
			}
			_ = os.Remove(bin + ".prev")
			return "bản khung " + bs.Version + " đã chạy tốt", nil
		}
		var st stamp
		if b, err := readRegular(filepath.Join(stateDir, startStamp), 1024); err == nil {
			_ = json.Unmarshal(b, &st)
		}
		started := st.Version == bs.Version && st.At.After(bs.PromotedAt)
		if started && now().Sub(st.At) >= RollbackAfter {
			if _, err := os.Stat(bin + ".prev"); err != nil {
				return "", fmt.Errorf("bản khung %s khởi động mà chưa chạy tốt, nhưng không còn bản cũ để quay về", bs.Version)
			}
			if err := os.Rename(bin+".prev", bin); err != nil {
				return "", err
			}
			bad := bs.Version
			bs = BootState{NoPromote: append(bs.NoPromote, bad)}
			if err := saveBoot(installDir, bs); err != nil {
				return "", err
			}
			return fmt.Sprintf("bản khung %s khởi động mà %s chưa chạy tốt: đã quay về bản khung cũ (có hiệu lực ở lần khởi động tới)",
				bad, RollbackAfter), nil
		}
		return "", nil // đang chờ bản khung mới khởi động / chạy tốt, chưa thay tiếp
	}

	// 2. Có bản nào đủ điều kiện làm bản khung không? current.json do agent ghi nên không tin:
	// mọi thứ lấy từ đó phải qua kiểm chữ ký bên dưới.
	cur, err := Load(stateDir)
	if err != nil || cur.Version == "" || !cur.Good || cur.GoodAt.IsZero() {
		return "", nil
	}
	if now().Sub(cur.GoodAt) < PromoteAfter || slices.Contains(bs.NoPromote, cur.Version) {
		return "", nil
	}
	if !protocol.ValidVersion(cur.Version) || cur.Arch != runtime.GOARCH || cur.Size <= 0 || cur.Size > protocol.MaxAgentBinary {
		return "", errors.New("thông tin bản cập nhật không hợp lệ")
	}
	installed, err := probeVersion(bin)
	if err != nil {
		return "", fmt.Errorf("không đọc được phiên bản bản khung đang cài: %w", err)
	}
	if installed == cur.Version {
		return "", nil
	}
	pub, err := LoadPub(pubPath)
	if err != nil {
		return "", err
	}
	data, err := readRegular(binPath(stateDir, cur.Version), protocol.MaxAgentBinary)
	if err != nil {
		return "", fmt.Errorf("đọc bản %s: %w", cur.Version, err)
	}
	if err := protocol.VerifyRelease(pub, cur.UpdateInfo, data); err != nil {
		return "", fmt.Errorf("bản %s không qua kiểm chữ ký, không thay bản khung: %w", cur.Version, err)
	}
	tmp := bin + ".new"
	defer os.Remove(tmp)
	if err := writeAtomic(tmp, data, 0o755); err != nil {
		return "", err
	}
	if v, err := probeVersion(tmp); err != nil || v != cur.Version {
		return "", fmt.Errorf("bản %s không chạy được trên máy này (version trả %q, lỗi %v)", cur.Version, v, err)
	}
	old, err := os.ReadFile(bin)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(bin+".prev", old, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, bin); err != nil {
		return "", err
	}
	bs.Version, bs.Prev, bs.PromotedAt, bs.Confirmed = cur.Version, installed, now().UTC(), false
	if err := saveBoot(installDir, bs); err != nil {
		return "", err
	}
	return fmt.Sprintf("đã thay bản khung %s bằng %s (bản cũ giữ ở %s.prev tới khi bản mới chạy tốt)", installed, cur.Version, bin), nil
}

func probeVersionOS(path string) (string, error) {
	out, err := exec.Command(path, "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

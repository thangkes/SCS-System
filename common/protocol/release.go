package protocol

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// releaseContext tách chữ ký bản phát hành khỏi chữ ký gói tin (cùng thuật toán, khác khóa).
const releaseContext = "scs-release/v1\n"

// MaxAgentBinary: kích thước tối đa một bản agent (chặn tải file khổng lồ).
const MaxAgentBinary = 64 << 20

// Arches: kiến trúc có bản agent.
var Arches = []string{"amd64", "arm64"}

var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

// ValidVersion: phiên bản dùng làm tên thư mục / đường dẫn URL nên chỉ cho ký tự an toàn.
func ValidVersion(v string) bool { return versionRe.MatchString(v) && !strings.Contains(v, "..") }

// ValidArch: amd64 hoặc arm64.
func ValidArch(a string) bool { return slices.Contains(Arches, a) }

// ReleaseSigningInput: nội dung được ký cho một bản agent (phiên bản + kiến trúc + SHA-256).
func ReleaseSigningInput(version, arch, sha256Hex string) []byte {
	return []byte(releaseContext + version + "\n" + arch + "\n" + sha256Hex)
}

// SignRelease ký một bản agent; trả chữ ký base64.
func SignRelease(priv ed25519.PrivateKey, version, arch, sha256Hex string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, ReleaseSigningInput(version, arch, sha256Hex)))
}

// VerifyRelease kiểm nội dung file (data) khớp UpdateInfo và chữ ký đúng khóa phát hành.
func VerifyRelease(pub ed25519.PublicKey, u UpdateInfo, data []byte) error {
	if !ValidVersion(u.Version) || !ValidArch(u.Arch) {
		return errors.New("phiên bản / kiến trúc không hợp lệ")
	}
	if int64(len(data)) != u.Size {
		return fmt.Errorf("kích thước %d byte, phải là %d", len(data), u.Size)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != u.SHA256 {
		return errors.New("SHA-256 không khớp")
	}
	sig, err := base64.StdEncoding.DecodeString(u.Sig)
	if err != nil || !ed25519.Verify(pub, ReleaseSigningInput(u.Version, u.Arch, u.SHA256), sig) {
		return errors.New("chữ ký bản phát hành không hợp lệ")
	}
	return nil
}

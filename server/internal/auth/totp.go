package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// TOTP (RFC 6238) cho app xác thực trên điện thoại (Google Authenticator, Microsoft Authenticator...):
// HMAC-SHA1, bước 30 giây, 6 chữ số — đúng mặc định mọi app đọc được. Server và điện thoại giữ chung một
// khóa bí mật 20 byte; không bên nào gửi gì cho bên kia, nên không cần mạng ra ngoài (SMTP, Internet).
//
// Khóa bí mật lưu trong DB đã mã hóa AES-256-GCM bằng khóa của server (SCS_TOTP_KEY, file 600 trong
// /etc/scs-server/pki): chỉ lộ bản sao lưu DB thì chưa tạo được mã.

const (
	totpPeriod  = 30
	totpDigits  = 6
	totpSkew    = 1 // nhận lệch ±1 bước (±30 giây) giữa giờ server và điện thoại
	totpKeySize = 20
	totpIssuer  = "SCS-System"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// totpStep: số bước 30 giây tính từ Unix epoch.
func totpStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// totpCode: mã 6 số của khóa ở bước step (RFC 4226 mục 5.3).
func totpCode(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	h := hmac.New(sha1.New, key)
	h.Write(msg[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000)
}

// totpMatch: mã khớp ở bước nào trong khoảng now ±totpSkew, chỉ nhận bước > after (chống dùng lại mã đã
// dùng). Trả bước khớp, ok. So khớp thời gian hằng (không lộ qua thời gian đáp ứng).
func totpMatch(key []byte, code string, now time.Time, after int64) (int64, bool) {
	if len(code) != totpDigits || !isDigits(code) {
		return 0, false
	}
	cur := totpStep(now)
	var found int64
	ok := 0
	for s := cur - totpSkew; s <= cur+totpSkew; s++ {
		eq := subtle.ConstantTimeCompare([]byte(totpCode(key, s)), []byte(code))
		if eq == 1 && s > after && found == 0 {
			found = s
		}
		ok |= eq
	}
	return found, ok == 1 && found != 0
}

// newTOTPKey: khóa bí mật ngẫu nhiên 160 bit.
func newTOTPKey() []byte {
	k := make([]byte, totpKeySize)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}

// totpURI: chuỗi otpauth:// trong mã QR. Chỉ ghi issuer + secret (thuật toán / số chữ số / chu kỳ là mặc
// định), để mã QR nhỏ, dễ quét.
func totpURI(username string, key []byte) string {
	label := url.PathEscape(totpIssuer + ":" + username)
	return "otpauth://totp/" + label + "?secret=" + b32.EncodeToString(key) + "&issuer=" + totpIssuer
}

// groupSecret: khóa dạng chữ chia nhóm 4 ký tự để gõ tay vào app khi không quét được QR.
func groupSecret(key []byte) string {
	s := b32.EncodeToString(key)
	var b strings.Builder
	for i := 0; i < len(s); i += 4 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s[i:min(i+4, len(s))])
	}
	return b.String()
}

// TOTPSealer mã hóa / giải mã khóa bí mật TOTP trước khi ghi DB.
type TOTPSealer struct{ aead cipher.AEAD }

// ErrTOTPKey: file khóa SCS_TOTP_KEY không hợp lệ.
var ErrTOTPKey = errors.New("SCS_TOTP_KEY phải là file chứa 64 ký tự hex (32 byte)")

// NewTOTPSealer tạo bộ mã hóa từ khóa 32 byte.
func NewTOTPSealer(key []byte) (*TOTPSealer, error) {
	if len(key) != 32 {
		return nil, ErrTOTPKey
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &TOTPSealer{aead: aead}, nil
}

// LoadTOTPSealer đọc file khóa (64 ký tự hex, chấp nhận xuống dòng cuối).
func LoadTOTPSealer(path string) (*TOTPSealer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, ErrTOTPKey
	}
	return NewTOTPSealer(key)
}

// seal: nonce || bản mã. Tên đăng nhập là dữ liệu kèm (AAD): bản mã chép sang tài khoản khác không giải được.
func (s *TOTPSealer) seal(username string, secret []byte) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return s.aead.Seal(nonce, nonce, secret, []byte("scs-totp\x00"+username))
}

func (s *TOTPSealer) open(username string, box []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(box) < n+s.aead.Overhead() {
		return nil, errors.New("khóa TOTP trong DB bị hỏng")
	}
	return s.aead.Open(nil, box[:n], box[n:], []byte("scs-totp\x00"+username))
}

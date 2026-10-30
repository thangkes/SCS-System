// Package fingerprint tính mã định danh máy và site_key.
//
// Fingerprint dùng để ĐỊNH DANH (máy này là máy nào), không dùng để xác thực:
// các thành phần đều đọc và sao chép được. Xác thực dựa vào khóa riêng của máy.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/netip"
	"strings"
)

const (
	fpContext   = "scs-fp/v1"
	siteContext = "scs-site/v1"
)

// Components là các thành phần tạo nên fingerprint.
// Server lưu riêng từng thành phần để biết thay đổi nằm ở đâu.
type Components struct {
	MachineID    string `json:"machine_id"`    // /etc/machine-id
	BoardSerial  string `json:"board_serial"`  // dmidecode -s baseboard-serial-number
	SystemSerial string `json:"system_serial"` // dmidecode -s system-serial-number
	PrimaryMAC   string `json:"primary_mac"`   // MAC card mạng chính
}

// Giá trị mặc định nhà sản xuất hay để lại trong DMI; coi như không có serial.
var placeholderSerials = map[string]bool{
	"":                         true,
	"0":                        true,
	"none":                     true,
	"n/a":                      true,
	"na":                       true,
	"default string":           true,
	"to be filled by o.e.m.":   true,
	"to be filled by oem":      true,
	"system serial number":     true,
	"base board serial number": true,
	"not specified":            true,
	"not applicable":           true,
	"0123456789":               true,
	"123456789":                true,
	"00000000":                 true,
}

// Normalize chuẩn hóa để cùng một máy luôn ra cùng một kết quả:
// bỏ khoảng trắng, chữ thường, serial giả thành rỗng, MAC dạng aa:bb:cc:dd:ee:ff.
func (c Components) Normalize() Components {
	return Components{
		MachineID:    strings.ToLower(strings.TrimSpace(c.MachineID)),
		BoardSerial:  normalizeSerial(c.BoardSerial),
		SystemSerial: normalizeSerial(c.SystemSerial),
		PrimaryMAC:   normalizeMAC(c.PrimaryMAC),
	}
}

// Hash trả về fingerprint dạng hex SHA-256 của các thành phần đã chuẩn hóa.
// Các trường được ngăn bằng byte 0 để "ab"+"c" không trùng với "a"+"bc".
func (c Components) Hash() string {
	n := c.Normalize()
	return hashParts(fpContext, n.MachineID, n.BoardSerial, n.SystemSerial, n.PrimaryMAC)
}

// Diff trả về tên các thành phần khác nhau giữa a và b (sau chuẩn hóa).
func Diff(a, b Components) []string {
	a, b = a.Normalize(), b.Normalize()
	var changed []string
	if a.MachineID != b.MachineID {
		changed = append(changed, "machine_id")
	}
	if a.BoardSerial != b.BoardSerial {
		changed = append(changed, "board_serial")
	}
	if a.SystemSerial != b.SystemSerial {
		changed = append(changed, "system_serial")
	}
	if a.PrimaryMAC != b.PrimaryMAC {
		changed = append(changed, "primary_mac")
	}
	return changed
}

// SiteKey ghép fingerprint với IP WAN mà SERVER quan sát được từ kết nối
// (agent không tự khai IP WAN). IPv4 dạng ::ffff:a.b.c.d được quy về a.b.c.d.
func SiteKey(fingerprint string, wanIP netip.Addr) string {
	return hashParts(siteContext, strings.ToLower(fingerprint), wanIP.Unmap().String())
}

func hashParts(context string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(context))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeSerial(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if placeholderSerials[s] {
		return ""
	}
	return s
}

func normalizeMAC(s string) string {
	s = strings.TrimSpace(s)
	if hw, err := net.ParseMAC(s); err == nil {
		return hw.String()
	}
	return strings.ToLower(s)
}

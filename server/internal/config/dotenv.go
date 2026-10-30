package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// LoadDotEnv đọc file .env và đặt các biến vào môi trường của tiến trình.
//
// Biến đã có sẵn trong môi trường (do systemd, docker hoặc shell đặt) được
// GIỮ NGUYÊN, không bị .env ghi đè. Trả về cảnh báo (ví dụ quyền file quá
// rộng) để người gọi ghi log; lỗi cú pháp chỉ báo số dòng, không in giá trị.
func LoadDotEnv(path string) (warnings []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if runtime.GOOS != "windows" {
		if st, err := f.Stat(); err == nil && st.Mode().Perm()&0o077 != 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%s có quyền %o, người khác đọc được mật khẩu; nên chạy: chmod 600 %s",
				path, st.Mode().Perm(), path))
		}
	}

	vars, err := parseDotEnv(f)
	if err != nil {
		return warnings, fmt.Errorf("%s: %w", path, err)
	}
	for k, v := range vars {
		if _, exists := os.LookupEnv(k); exists {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

// parseDotEnv hỗ trợ:
//
//	KEY=value            # chú thích sau dấu cách + #
//	export KEY=value
//	KEY="có dấu cách, \n xuống dòng, \" ngoặc kép"
//	KEY='nguyên văn, không xử lý ký tự đặc biệt'
func parseDotEnv(r io.Reader) (map[string]string, error) {
	vars := map[string]string{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if n == 1 {
			line = strings.TrimPrefix(line, string(rune(0xFEFF))) // BOM do Notepad trên Windows
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !validKey(key) {
			return nil, fmt.Errorf("dòng %d: cần dạng KEY=VALUE", n)
		}
		val, err := parseValue(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("dòng %d (%s): %w", n, key, err)
		}
		vars[key] = val
	}
	return vars, sc.Err()
}

func parseValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	switch raw[0] {
	case '\'':
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 {
			return "", fmt.Errorf("thiếu dấu ' đóng")
		}
		return raw[1 : end+1], nil
	case '"':
		var b strings.Builder
		for i := 1; i < len(raw); i++ {
			c := raw[i]
			if c == '"' {
				return b.String(), nil
			}
			if c == '\\' && i+1 < len(raw) {
				i++
				switch raw[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default: // \" \\ \$ ...
					b.WriteByte(raw[i])
				}
				continue
			}
			b.WriteByte(c)
		}
		return "", fmt.Errorf("thiếu dấu \" đóng")
	}
	// Không có ngoặc: " #" bắt đầu chú thích. Mật khẩu chứa " #" thì để trong ngoặc.
	// "KEY=   # chú thích": raw đã bỏ khoảng trắng nên bắt đầu bằng '#' → giá trị rỗng.
	if raw[0] == '#' {
		return "", nil
	}
	if i := strings.Index(raw, " #"); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(raw), nil
}

func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i, c := range k {
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

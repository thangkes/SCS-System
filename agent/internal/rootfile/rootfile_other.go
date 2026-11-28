//go:build !unix

package rootfile

import "os"

// Hệ không phải unix (chỉ để build / test trên máy phát triển): không có khái niệm chủ root, đọc như thường.
func readRoot(p string, _ int64) ([]byte, error) { return os.ReadFile(p) }

//go:build !unix

package watch

import "os"

// fileID: ngoài Unix không có inode; chỉ nhận ra file bị cắt ngắn (kích thước nhỏ lại).
func fileID(os.FileInfo) string { return "" }

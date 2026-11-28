//go:build !unix

package watch

import "os"

// fileID: ngoài Unix không có inode; chỉ nhận ra file bị cắt ngắn (kích thước nhỏ lại).
func fileID(os.FileInfo) string { return "" }

// plainReadable: ngoài Unix không có CAP_DAC_READ_SEARCH: quyền mở file là quyền thật.
func plainReadable(os.FileInfo) bool { return true }

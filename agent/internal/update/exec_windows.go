//go:build windows

package update

import "errors"

// execBinary: Windows chỉ dùng để thử nghiệm, không tự chuyển bản.
func execBinaryOS(string) error { return errors.New("Windows không hỗ trợ tự cập nhật") }

//go:build unix

package watch

import (
	"os"
	"strconv"
	"syscall"
)

// fileID: thiết bị + inode, đổi khi logrotate thay file mới vào đường dẫn cũ.
func fileID(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(st.Dev), 16) + ":" + strconv.FormatUint(uint64(st.Ino), 16)
	}
	return ""
}

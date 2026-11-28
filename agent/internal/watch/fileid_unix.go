//go:build unix

package watch

import (
	"os"
	"slices"
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

// plainReadable: agent đọc được file này bằng quyền thường (chủ file / nhóm / người khác), KHÔNG nhờ
// CAP_DAC_READ_SEARCH. Quyền đó chỉ cấp để đọc serial phần cứng; nếu không kiểm, ai làm chủ một thư mục trong
// /var/log (user của ứng dụng) có thể đổi file log rule đang đọc thành symlink tới file chỉ root đọc được
// (/etc/shadow…), và dòng khớp rule sẽ thành dòng mẫu gửi lên server. Kiểm trên file đã mở (fi từ f.Stat()) nên
// symlink, hardlink hay tráo file giữa chừng đều không lách được. Root thì vốn đọc được mọi thứ: không kiểm.
func plainReadable(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	uid := os.Geteuid()
	if uid == 0 {
		return true
	}
	mode := fi.Mode().Perm()
	if int(st.Uid) == uid {
		return mode&0o400 != 0
	}
	if inGroup(int(st.Gid)) {
		return mode&0o040 != 0
	}
	return mode&0o004 != 0
}

func inGroup(gid int) bool {
	if os.Getegid() == gid {
		return true
	}
	gs, err := os.Getgroups()
	return err == nil && slices.Contains(gs, gid)
}

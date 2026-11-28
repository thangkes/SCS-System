// Package rootfile đọc các file mà agent phải tin: cấu hình (agent.env), CA của server, khóa công khai phát hành.
//
// Vì sao: thư mục /etc/scs-agent thuộc user scs-agent (agent phải ghi được khóa, chứng chỉ máy vào đó). Ai có
// quyền ghi thư mục thì xóa và thay được file trong đó, kể cả file của root. Kẻ chiếm được user scs-agent nhờ vậy
// có thể thay agent.env / server-ca.pem để trỏ agent sang server giả, hoặc đổi đường dẫn khóa phát hành.
// File mới tạo ra luôn thuộc scs-agent, không thuộc root, nên chỉ cần đòi các file này thuộc root là chặn được.
//
// Chỉ đòi với file ở vị trí hệ thống (/etc/, /opt/) — nơi trình cài đặt (root) đặt chúng. File ở chỗ khác (thử
// trước khi cài, test) đọc như thường.
package rootfile

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

// MaxSize: các file tin cậy đều nhỏ (vài KB); chặn đọc file lớn bất thường.
const MaxSize = 1 << 20

// Required: file ở vị trí hệ thống thì phải thuộc root.
func Required(p string) bool {
	c := path.Clean(p) // đường dẫn kiểu Linux (gạch chéo xuôi) trên mọi hệ build
	return strings.HasPrefix(c, "/etc/") || strings.HasPrefix(c, "/opt/")
}

// Read đọc file tin cậy. Với file ở vị trí hệ thống: bản thân file không phải symlink, phải là
// file thường, thuộc root, nhóm / người khác không ghi được, không quá MaxSize. Kiểm trên chính file đã mở nên không
// bị tráo giữa lúc kiểm và lúc đọc.
func Read(name string) ([]byte, error) {
	if !Required(name) {
		return os.ReadFile(name)
	}
	// Chỉ thư mục /etc/scs-agent là user scs-agent ghi được, và các file tin cậy nằm ngay trong đó: chặn symlink ở
	// phần cuối đường dẫn (O_NOFOLLOW) là đủ. Không đòi cả đường dẫn không có symlink, để máy có /opt trỏ sang ổ khác
	// không bị từ chối oan.
	b, err := readRoot(path.Clean(name), MaxSize)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("%s: %w — file này phải do root đặt (cài lại bằng install.sh nếu cần)", name, err)
	}
	return b, nil
}

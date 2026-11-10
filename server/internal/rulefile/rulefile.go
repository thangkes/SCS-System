// Package rulefile đọc file rule YAML do admin viết thành rules.RuleSet (common/rules).
//
// YAML chỉ là cách viết cho dễ đọc: nội dung được chuyển qua JSON rồi đọc vào đúng kiểu dùng
// chung với agent, với chế độ từ chối trường lạ — gõ sai tên trường (ví dụ "serverity") là lỗi
// ngay chứ không bị bỏ qua lặng lẽ.
package rulefile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/thangkes/SCS-System/common/rules"
)

// MaxFile: kích thước tối đa một file rule.
const MaxFile = 2 << 20

// Load đọc và kiểm một file rule YAML (Validate, chưa chạy tests).
func Load(path string) (*rules.RuleSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse đọc nội dung YAML (JSON cũng là YAML hợp lệ).
func Parse(b []byte) (*rules.RuleSet, error) {
	if len(b) > MaxFile {
		return nil, fmt.Errorf("file rule %d byte, tối đa %d", len(b), MaxFile)
	}
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("YAML sai: %w", err)
	}
	j, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("YAML có kiểu không chuyển được (khóa phải là chữ): %w", err)
	}
	var s rules.RuleSet
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("nội dung sai: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

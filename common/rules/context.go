package rules

import (
	"fmt"
	"slices"
	"strings"
)

// Context: ngữ cảnh của máy chạy agent, dùng để chọn rule theo applies_to.
//
// Khóa chuẩn (agent tự suy ra, server ghi đè được bằng nhãn trong cấu hình agent 3 cấp):
//   - role:  pri | sec | phu   (từ hostname <mã>-TN1-PRI / -TN2-SEC / -TN3...)
//   - store: mã siêu thị       (từ hostname)
//   - os:    ví dụ ubuntu-22.04 (từ /etc/os-release: ID-VERSION_ID)
//
// Có thể thêm khóa tự do từ server (ví dụ vung=mien-nam).
type Context map[string]string

// Khóa ngữ cảnh chuẩn.
const (
	CtxRole  = "role"
	CtxStore = "store"
	CtxOS    = "os"
)

// ValidContextKey / ValidContextValue: dạng khóa và giá trị ngữ cảnh (cũng là dạng trong applies_to).
func ValidContextKey(k string) bool   { return ctxKeyRe.MatchString(k) }
func ValidContextValue(v string) bool { return ctxValRe.MatchString(v) }

// Applies: rule có chạy trong ngữ cảnh ctx không. ctx nil = không lọc (server chạy thử mọi rule).
func (r *Rule) Applies(ctx Context) bool {
	if ctx == nil || len(r.AppliesTo) == 0 {
		return true
	}
	for k, vals := range r.AppliesTo {
		v, ok := ctx[k]
		if !ok || !slices.Contains(vals, v) {
			return false
		}
	}
	return true
}

// ParseContext đọc "role=pri,os=ubuntu-22.04" (lệnh rules test -ctx). Chuỗi rỗng = nil (không lọc).
func ParseContext(s string) (Context, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	ctx := Context{}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok || !ValidContextKey(k) || !ValidContextValue(v) {
			return nil, fmt.Errorf("ngữ cảnh %q: phải có dạng khoa=gia_tri, ví dụ role=pri,os=ubuntu-22.04", kv)
		}
		ctx[k] = v
	}
	return ctx, nil
}

// String: "os=ubuntu-22.04 role=pri store=12345" (sắp xếp theo khóa).
func (c Context) String() string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + c[k]
	}
	return strings.Join(parts, " ")
}

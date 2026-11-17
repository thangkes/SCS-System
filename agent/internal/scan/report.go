package scan

import (
	"encoding/json"
	"errors"
	"os"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/common/rules"
)

// Report: bộ rule đang cài trên máy này, gửi kèm heartbeat để web biết máy đã áp dụng bộ nào.
// Kiểm chữ ký như lúc nạp: bộ hỏng / sai chữ ký thì báo Installed=false kèm lỗi.
// Scanning luôn false cho tới mốc A3 (agent chưa đọc log liên tục).
func Report(stateDir, pubPath string, labels map[string]string) *protocol.RuleSetReport {
	r := &protocol.RuleSetReport{}
	data, err := os.ReadFile(BundlePath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return r // chưa cài bộ nào
	}
	var b rules.Bundle
	if err == nil && len(data) <= 2*rules.MaxBundle {
		_ = json.Unmarshal(data, &b)
		r.Name, r.Version, r.SHA256 = b.Name, b.Version, b.SHA256
	}
	set, err := Load(stateDir, pubPath)
	if err != nil {
		r.Error = cut(err.Error(), 200)
		return r
	}
	r.Installed, r.Name, r.Version = true, set.Name, set.Version
	for _, rl := range set.Rules {
		if !rl.Disabled {
			r.Total++
		}
	}
	if e, err := rules.CompileFor(set, LocalContext(labels)); err == nil {
		r.Active = r.Total - len(e.Skipped())
	}
	return r
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

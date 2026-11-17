package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/common/protocol"
)

// HeartbeatRecord: những gì một heartbeat ghi vào kho (docs/thiet-ke-luu-theo-giai-doan.md mục 7).
// Trạng thái mới nhất ghi đè một dòng mỗi máy; chỉ khi Sig khác lần trước mới ghi thêm Summary
// vào lịch sử trạng thái.
type HeartbeatRecord struct {
	IP             netip.Addr
	AgentVersion   string
	RuleSetVersion string
	UptimeS        int64
	AgentTime      time.Time
	QueuePending   int
	Status         json.RawMessage         // MachineStatus (nil = agent không gửi)
	Rules          *protocol.RuleSetReport // bộ rule đang cài (nil = agent bản cũ, không báo)
	Sig            string                  // dấu các trường đáng kể (SHA-256 hex)
	Summary        map[string]any          // các trường đáng kể, ghi vào lịch sử khi Sig đổi
}

// StatusChange: một lần trạng thái máy thay đổi đáng kể.
type StatusChange struct {
	At      time.Time       `json:"at"`
	Changes json.RawMessage `json:"changes"`
}

// maxStatusJSON: trạng thái máy lưu trong device_status.status (DB chặn 16 KB).
const maxStatusJSON = 15000

// diskLevel: mức đầy ổ đĩa tính theo mốc 80 / 90 / 95% (đổi mốc mới ghi lịch sử).
func diskLevel(d protocol.DiskStatus) int {
	if d.TotalBytes == 0 {
		return 0
	}
	used := float64(d.TotalBytes-min(d.FreeBytes, d.TotalBytes)) * 100 / float64(d.TotalBytes)
	switch {
	case used >= 95:
		return 95
	case used >= 90:
		return 90
	case used >= 80:
		return 80
	}
	return 0
}

// statusSummary: các trường đáng kể của heartbeat và dấu của chúng. Không gồm RAM, tải CPU, uptime,
// dung lượng trống (đổi liên tục): chỉ hệ điều hành, kernel, bản agent, bộ rule, IP, dịch vụ, RAID,
// đồng bộ giờ, ổ đĩa vượt mốc.
func statusSummary(agentVersion string, ip netip.Addr, body protocol.HeartbeatBody) (map[string]any, string) {
	m := map[string]any{"agent": agentVersion, "ip": ip.String()}
	if r := body.Rules; r != nil {
		switch {
		case r.Installed:
			m["rules"] = r.Name + "@" + r.Version + " (" + shortSHA(r.SHA256) + ")"
		case r.Error != "":
			m["rules"] = "lỗi: " + r.Error
		default:
			m["rules"] = "chưa cài"
		}
	} else if body.RuleSetVersion != "" {
		m["rules"] = body.RuleSetVersion
	}
	if st := body.Status; st != nil {
		m["os"], m["kernel"] = st.OS, st.Kernel
		m["time_synced"] = st.TimeSync.Synced
		svcs := map[string]string{}
		for _, s := range st.Services {
			svcs[s.Name] = s.Active
		}
		m["services"] = svcs
		raid := map[string]string{}
		for _, r := range st.RAID {
			state := "ok"
			switch {
			case r.Degraded:
				state = "degraded"
			case r.Sync != "":
				state = "dong-bo"
			}
			raid[r.Name] = state
		}
		m["raid"] = raid
		disks := map[string]int{}
		for _, d := range st.Disks {
			if lv := diskLevel(d); lv > 0 {
				disks[d.Mount] = lv
			}
		}
		m["disks_full"] = disks
	}
	// encoding/json xếp khóa map theo thứ tự chữ cái, nên cùng nội dung luôn ra cùng dấu.
	b, _ := json.Marshal(m)
	sum := sha256.Sum256(b)
	return m, hex.EncodeToString(sum[:])
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

var (
	reportNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	reportVerRe  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
	reportSHARe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// cleanReport: kiểm báo cáo bộ rule của agent; trường sai định dạng bị bỏ, không làm hỏng heartbeat.
func cleanReport(r *protocol.RuleSetReport) *protocol.RuleSetReport {
	if r == nil {
		return nil
	}
	c := *r
	if !reportNameRe.MatchString(c.Name) || !reportVerRe.MatchString(c.Version) {
		c.Name, c.Version = "", ""
	}
	if !reportSHARe.MatchString(c.SHA256) {
		c.SHA256 = ""
	}
	if c.Name == "" || c.SHA256 == "" {
		c.Installed = false
	}
	c.Total = min(max(c.Total, 0), 10000)
	c.Active = min(max(c.Active, 0), c.Total)
	c.Error = cleanLine(c.Error)
	if len(c.Error) > 200 {
		c.Error = c.Error[:200]
		for !utf8.ValidString(c.Error) {
			c.Error = c.Error[:len(c.Error)-1]
		}
	}
	return &c
}

// heartbeatRecord dựng bản ghi từ heartbeat đã kiểm chữ ký.
func heartbeatRecord(env protocol.Envelope, body protocol.HeartbeatBody, ip netip.Addr) HeartbeatRecord {
	body.Rules = cleanReport(body.Rules)
	summary, sig := statusSummary(env.AgentVersion, ip, body)
	h := HeartbeatRecord{IP: ip, AgentVersion: env.AgentVersion, RuleSetVersion: body.RuleSetVersion,
		UptimeS: max(body.UptimeS, 0), AgentTime: env.TS, QueuePending: max(body.LogQueuePending, 0),
		Rules: body.Rules, Sig: sig, Summary: summary}
	if body.Status != nil {
		if b, err := json.Marshal(body.Status); err == nil && len(b) <= maxStatusJSON {
			h.Status = b
		}
	}
	return h
}

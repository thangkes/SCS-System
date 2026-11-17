package app

import (
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/periods"
)

// maxLogEvents: số sự kiện tối đa một lô (agent đã gom theo aggregate / cooldown của rule).
const maxLogEvents = 500

var logRuleIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// logsHandler xử lý POST /v1/logs: lô sự kiện khớp rule của máy đã duyệt. Không lưu từng sự kiện:
// cộng vào giai đoạn đang mở của máy (docs/thiet-ke-luu-theo-giai-doan.md). Sự kiện sai định dạng bị
// bỏ (không làm hỏng cả lô); giờ sự kiện ở tương lai bị kéo về giờ server.
func logsHandler(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env, id, ok := readAgentPacket(d, w, r, protocol.TypeLogBatch)
		if !ok {
			return
		}
		var body protocol.LogBatchBody
		if err := env.DecodeBody(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "nội dung lô sự kiện sai định dạng")
			return
		}
		if len(body.Events) > maxLogEvents {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "lô quá nhiều sự kiện")
			return
		}
		evs := toPeriodEvents(body.Events, time.Now())
		if err := d.Registry.AddEvents(r.Context(), id, evs); err != nil {
			d.Log.Error("ghi sự kiện thất bại", "err", err, "device", id)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		activity(d).Logs(len(evs))
		httpx.WriteJSON(w, http.StatusOK, protocol.Response{OK: true, ServerTime: time.Now().UTC()})
	})
}

// toPeriodEvents kiểm và đổi sự kiện của agent sang dạng của gói periods.
func toPeriodEvents(in []protocol.LogEvent, now time.Time) []periods.Event {
	out := make([]periods.Event, 0, len(in))
	for _, e := range in {
		if !logRuleIDRe.MatchString(e.RuleID) || !periods.ValidSeverity(e.Severity) {
			continue
		}
		t := e.Time
		// Quá cũ (agent giữ khi mất mạng lâu) hoặc ở tương lai: tính vào giờ hiện tại.
		if t.IsZero() || t.After(now) || now.Sub(t) > 7*24*time.Hour {
			t = now
		}
		n := e.Count
		if n < 1 {
			n = 1
		}
		out = append(out, periods.Event{RuleID: e.RuleID, Severity: e.Severity, Count: min(n, 1_000_000),
			First: t, Last: t, Sample: cleanLine(e.Line)})
	}
	return out
}

// cleanLine: dòng log mẫu chỉ giữ UTF-8 hợp lệ, bỏ ký tự điều khiển, tối đa periods.MaxSample byte.
func cleanLine(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > periods.MaxSample {
		s = s[:periods.MaxSample]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

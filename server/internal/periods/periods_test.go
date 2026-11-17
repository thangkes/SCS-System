package periods

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

func at(h, m int) time.Time { return t0.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }

func ev(rule, sev string, when time.Time) Event {
	return Event{RuleID: rule, Severity: sev, Count: 1, First: when, Last: when, Sample: rule + " dòng mẫu"}
}

// apply: ghi Result như DB sẽ ghi, trả giai đoạn đang mở và danh sách đã đóng.
type timeline struct {
	cur     *Period
	closed  []*Period
	offline int // số cảnh báo mất liên lạc đang mở
	nextID  int64
}

func (tl *timeline) apply(t *testing.T, r Result) {
	t.Helper()
	if r.Closed != nil {
		if r.Closed.EndedAt.IsZero() || r.Closed.EndedAt.Before(r.Closed.StartedAt) {
			t.Fatalf("giai đoạn đóng sai giờ: %+v", r.Closed)
		}
		tl.closed = append(tl.closed, r.Closed)
	}
	if r.Opened != nil {
		tl.nextID++
		r.Opened.ID = tl.nextID
		tl.cur = r.Opened
	} else if r.Updated != nil {
		tl.cur = r.Updated
	} else if r.Closed != nil {
		tl.cur = nil
	}
	tl.offline += r.OfflineAlert
	if tl.offline < 0 || tl.offline > 1 {
		t.Fatalf("số cảnh báo mất liên lạc sai: %d", tl.offline)
	}
}

func (tl *timeline) seen(t *testing.T, now time.Time) { tl.apply(t, Seen(tl.cur, 7, now)) }
func (tl *timeline) events(t *testing.T, now time.Time, e ...Event) {
	tl.seen(t, now)
	tl.apply(t, AddEvents(tl.cur, 7, e, now))
}
func (tl *timeline) tick(t *testing.T, lastSeen, now time.Time) {
	tl.apply(t, Tick(tl.cur, lastSeen, now, DefaultPolicy))
}

// Ví dụ trong tài liệu thiết kế: OK kéo dài, high mở cảnh báo, critical chỉ admin đóng.
func TestDesignExample(t *testing.T) {
	var tl timeline
	tl.events(t, at(0, 0), ev("SVC-FAILED-01", "medium", at(0, 0)))
	tl.events(t, at(1, 0), ev("SVC-FAILED-01", "medium", at(1, 0)))
	tl.seen(t, at(2, 0))
	if tl.cur.Kind != KindOK || tl.cur.Rules["SVC-FAILED-01"].Count != 2 || len(tl.closed) != 0 || tl.cur.StartedAt != at(0, 0) {
		t.Fatalf("OK phải là một giai đoạn kéo dài: %+v", tl.cur)
	}
	tl.events(t, at(2, 20), ev("SSH-ROOT-LOGIN-01", "high", at(2, 20)))
	if tl.cur.Kind != KindAlert || len(tl.closed) != 1 || tl.closed[0].EndedAt != at(2, 20) || tl.cur.StartedAt != at(2, 20) {
		t.Fatalf("high phải đóng OK và mở cảnh báo: cur %+v closed %+v", tl.cur, tl.closed)
	}
	tl.events(t, at(2, 40), ev("SVC-FAILED-01", "medium", at(2, 40)))
	tl.events(t, at(3, 5), ev("RAID-01", "critical", at(3, 5)))
	if tl.cur.Kind != KindAlert || tl.cur.Severity != "critical" || tl.cur.Events != 3 || len(tl.closed) != 1 {
		t.Fatalf("sự kiện sau phải cộng vào cảnh báo: %+v", tl.cur)
	}
	// Critical: hết hạn tự đóng vẫn không đóng.
	tl.seen(t, at(30, 0))
	tl.tick(t, at(30, 0), at(30, 1))
	if tl.cur.Kind != KindAlert {
		t.Fatal("critical không được tự đóng")
	}
	r, err := Resolve(tl.cur, "Thu", "đã thay ổ", at(30, 0), at(30, 2), DefaultPolicy)
	if err != nil {
		t.Fatal(err)
	}
	tl.apply(t, r)
	if tl.cur.Kind != KindOK || tl.closed[1].ClosedBy != "admin:Thu" || tl.closed[1].Note != "đã thay ổ" {
		t.Fatalf("admin xử lý: %+v %+v", tl.cur, tl.closed[1])
	}
}

func TestHighAutoClose(t *testing.T) {
	var tl timeline
	tl.events(t, at(0, 0), ev("SSH-ROOT-LOGIN-01", "high", at(0, 0)))
	if tl.cur.Kind != KindAlert || len(tl.closed) != 0 {
		t.Fatalf("máy mới, sự kiện đầu là high: mở thẳng cảnh báo: %+v %v", tl.cur, tl.closed)
	}
	// Sự kiện high mới làm lùi hạn tự đóng; medium không làm lùi.
	tl.events(t, at(10, 0), ev("SSH-ROOT-LOGIN-01", "high", at(10, 0)))
	tl.events(t, at(20, 0), ev("X", "medium", at(20, 0)))
	tl.tick(t, at(20, 0), at(33, 0))
	if tl.cur.Kind != KindAlert {
		t.Fatal("chưa đủ 24 giờ kể từ high cuối mà đã đóng")
	}
	tl.seen(t, at(34, 0))
	tl.tick(t, at(34, 0), at(34, 1))
	if tl.cur.Kind != KindOK || tl.closed[0].ClosedBy != ClosedAuto {
		t.Fatalf("high phải tự đóng sau 24 giờ: %+v %+v", tl.cur, tl.closed)
	}
	// Đã từng critical rồi chỉ còn high: vẫn chỉ admin đóng.
	tl.events(t, at(40, 0), ev("A", "critical", at(40, 0)), ev("B", "high", at(40, 1)))
	tl.seen(t, at(90, 0))
	tl.tick(t, at(90, 0), at(90, 1))
	if tl.cur.Kind != KindAlert || !tl.cur.AdminOnly() {
		t.Fatal("cảnh báo có critical không được tự đóng")
	}
}

func TestOffline(t *testing.T) {
	var tl timeline
	tl.seen(t, at(0, 0))
	tl.tick(t, at(0, 0), at(0, 14))
	if tl.cur.Kind != KindOK || tl.offline != 0 {
		t.Fatal("chưa quá 15 phút")
	}
	tl.tick(t, at(0, 0), at(0, 16))
	if tl.cur.Kind != KindOffline || tl.offline != 1 || tl.closed[0].EndedAt != at(0, 0) || tl.cur.StartedAt != at(0, 0) {
		t.Fatalf("mất liên lạc: %+v %+v", tl.cur, tl.closed)
	}
	tl.tick(t, at(0, 0), at(5, 0)) // tick lặp lại không mở thêm
	tl.seen(t, at(5, 3))
	if tl.cur.Kind != KindOK || tl.offline != 0 || tl.closed[1].Kind != KindOffline || tl.closed[1].EndedAt != at(5, 3) {
		t.Fatalf("máy gửi lại phải tự đóng mất liên lạc: %+v %+v", tl.cur, tl.closed)
	}
}

// Mất liên lạc khi đang cảnh báo: cảnh báo giữ nguyên, chỉ đánh dấu; máy gửi lại thì bỏ đánh dấu.
func TestOfflineDuringAlert(t *testing.T) {
	var tl timeline
	tl.events(t, at(0, 0), ev("A", "critical", at(0, 0)))
	tl.tick(t, at(0, 0), at(1, 0))
	if tl.cur.Kind != KindAlert || tl.cur.OfflineSince != at(0, 0) || tl.offline != 1 || len(tl.closed) != 0 {
		t.Fatalf("%+v", tl.cur)
	}
	tl.tick(t, at(0, 0), at(2, 0))
	tl.seen(t, at(2, 5))
	if tl.cur.Kind != KindAlert || !tl.cur.OfflineSince.IsZero() || tl.offline != 0 {
		t.Fatalf("máy gửi lại: %+v", tl.cur)
	}
	// Cảnh báo high hết hạn trong lúc mất liên lạc: đóng, mở giai đoạn mất liên lạc (cảnh báo vẫn mở).
	var tl2 timeline
	tl2.events(t, at(0, 0), ev("A", "high", at(0, 0)))
	tl2.tick(t, at(0, 0), at(1, 0))
	tl2.tick(t, at(0, 0), at(25, 0))
	if tl2.cur.Kind != KindOffline || tl2.offline != 1 || tl2.closed[0].ClosedBy != ClosedAuto {
		t.Fatalf("%+v %+v", tl2.cur, tl2.closed)
	}
	tl2.seen(t, at(26, 0))
	if tl2.cur.Kind != KindOK || tl2.offline != 0 {
		t.Fatal("máy gửi lại sau khi cảnh báo tự đóng")
	}
}

func TestEventsInOneBatch(t *testing.T) {
	var tl timeline
	tl.seen(t, at(0, 0))
	// Một lô: medium trước, high sau → medium thuộc OK, high mở cảnh báo đúng giờ của nó.
	tl.events(t, at(1, 0), ev("M", "medium", at(0, 30)), ev("H", "high", at(0, 50)), ev("L", "low", at(0, 55)))
	if len(tl.closed) != 1 || tl.closed[0].Rules["M"] == nil || tl.closed[0].Rules["H"] != nil || tl.closed[0].EndedAt != at(0, 50) {
		t.Fatalf("OK đóng: %+v", tl.closed)
	}
	if tl.cur.Kind != KindAlert || tl.cur.StartedAt != at(0, 50) || tl.cur.Rules["H"] == nil || tl.cur.Rules["L"] == nil {
		t.Fatalf("cảnh báo: %+v", tl.cur)
	}
	// Sự kiện mang giờ tương lai (đồng hồ máy lệch) bị kéo về giờ server.
	tl.events(t, at(2, 0), ev("F", "medium", at(9, 0)))
	if tl.cur.Rules["F"].Last != at(2, 0) {
		t.Fatalf("giờ tương lai: %+v", tl.cur.Rules["F"])
	}
}

func TestLimits(t *testing.T) {
	var tl timeline
	tl.seen(t, at(0, 0))
	var evs []Event
	for i := 0; i < MaxRules+10; i++ {
		evs = append(evs, ev("R-"+strings.Repeat("x", i%5)+string(rune('A'+i%26))+string(rune('a'+i/26)), "low", at(0, 1)))
	}
	big := ev("BIG", "low", at(0, 2))
	big.Sample = strings.Repeat("ă", MaxSample) // 2 byte mỗi ký tự
	evs = append(evs, big, Event{RuleID: "BAD", Severity: "lung-tung"}, Event{Severity: "low"})
	tl.events(t, at(0, 5), evs...)
	if len(tl.cur.Rules) > MaxRules+1 || tl.cur.Rules[OtherRule] == nil {
		t.Fatalf("giới hạn rule: %d", len(tl.cur.Rules))
	}
	if tl.cur.Rules["BAD"] != nil {
		t.Fatal("mức lạ phải bị bỏ")
	}
	for _, s := range tl.cur.Rules {
		if len(s.Sample) > MaxSample {
			t.Fatal("dòng mẫu quá dài")
		}
	}
	if _, err := Resolve(tl.cur, "a", "", at(0, 5), at(0, 6), DefaultPolicy); err != ErrNotAlert {
		t.Fatal("chỉ xử lý được cảnh báo")
	}
	r := Retire(tl.cur, at(1, 0))
	if r.Closed == nil || r.Opened != nil {
		t.Fatal("thu hồi máy: đóng, không mở mới")
	}
}

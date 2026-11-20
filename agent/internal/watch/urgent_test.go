package watch

import "testing"

// Chỉ sự kiện làm máy chuyển trạng thái mới gửi gấp: lần đầu high (máy đang OK), lên critical; cùng mức /
// nhẹ hơn thì gom theo nhịp thường; server báo sự cố đã đóng (mức rỗng) thì sự cố mới lại gửi gấp.
func TestUrgent(t *testing.T) {
	s := &Scanner{}
	steps := []struct {
		set  *string // đặt mức từ server trước bước này
		sev  string
		want bool
	}{
		{nil, "medium", false},
		{nil, "high", true},
		{nil, "high", false},
		{nil, "medium", false},
		{nil, "critical", true},
		{nil, "high", false},
		{ptr(""), "high", true}, // sự cố đã đóng
		{ptr("critical"), "critical", false},
		{ptr("bậy"), "high", true}, // mức lạ coi như OK: gửi gấp cho chắc
	}
	for i, st := range steps {
		if st.set != nil {
			s.SetAlertLevel(*st.set)
		}
		if got := s.urgent(st.sev); got != st.want {
			t.Fatalf("bước %d (%s, mức server %q): gửi gấp = %v, cần %v", i, st.sev, s.alertLevel(), got, st.want)
		}
	}
}

func ptr(s string) *string { return &s }

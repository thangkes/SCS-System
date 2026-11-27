package rules

import (
	"net/netip"
	"strings"
	"testing"
)

func lanSet(l *LAN) *RuleSet { return &RuleSet{Schema: LANSchema, Name: "lan", Version: "1", LAN: l} }

func TestLANValidate(t *testing.T) {
	ok := lanSet(&LAN{Targets: []LANTarget{
		{IP: "192.168.1.10", Type: "can", Label: "Cân 1"},
		{IP: "192.168.1.50", Type: "may_in", Label: "Máy in", Kind: "tcp", Port: 9100},
		{IP: "192.168.1.31", Type: "tn"},
	}})
	if err := ok.Validate(); err != nil {
		t.Fatalf("bộ hợp lệ (không rule nào, chỉ sơ đồ LAN) bị từ chối: %v", err)
	}
	if _, err := Compile(ok); err != ErrNoRules {
		t.Fatalf("bộ chỉ có sơ đồ LAN: Compile phải trả ErrNoRules (không có gì để quét log), được %v", err)
	}
	many := make([]LANTarget, MaxLANTargets+1)
	for i := range many {
		many[i] = LANTarget{IP: netip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)}).String(), Type: "khac"}
	}
	bad := []struct {
		name string
		set  *RuleSet
		want string
	}{
		{"schema cũ", &RuleSet{Schema: 3, Name: "lan", Version: "1", LAN: ok.LAN}, "schema: 4"},
		{"IP công khai", lanSet(&LAN{Targets: []LANTarget{{IP: "8.8.8.8", Type: "khac"}}}), "IPv4 nội bộ"},
		{"địa chỉ mạng", lanSet(&LAN{Targets: []LANTarget{{IP: "192.168.1.0", Type: "khac"}}}), "IPv4 nội bộ"},
		{"broadcast", lanSet(&LAN{Targets: []LANTarget{{IP: "192.168.1.255", Type: "khac"}}}), "IPv4 nội bộ"},
		{"dải", lanSet(&LAN{Targets: []LANTarget{{IP: "192.168.1.0/24", Type: "khac"}}}), "không dải IP"},
		{"loại lạ", lanSet(&LAN{Targets: []LANTarget{{IP: "10.0.0.1", Type: "xe"}}}), "type"},
		{"nhãn có <", lanSet(&LAN{Targets: []LANTarget{{IP: "10.0.0.1", Type: "khac", Label: "<b>"}}}), "label"},
		{"trùng", lanSet(&LAN{Targets: []LANTarget{{IP: "10.0.0.1", Type: "khac"}, {IP: "10.0.0.1", Type: "can"}}}), "trùng"},
		{"tcp thiếu cổng", lanSet(&LAN{Targets: []LANTarget{{IP: "10.0.0.1", Type: "khac", Kind: "tcp"}}}), "port"},
		{"nhịp quá dày", lanSet(&LAN{EveryHB: 1, Targets: []LANTarget{{IP: "10.0.0.1", Type: "khac"}}}), "every_hb"},
		{"thử lại nhiều", lanSet(&LAN{Retry: 5, Targets: []LANTarget{{IP: "10.0.0.1", Type: "khac"}}}), "retry"},
		{"quá nhiều đích", lanSet(&LAN{Targets: many}), "targets"},
		{"trống", lanSet(&LAN{}), "targets"},
	}
	for _, c := range bad {
		err := c.set.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: muốn lỗi chứa %q, được %v", c.name, c.want, err)
		}
	}
}

// ListV đổi khi thứ tự / IP / cổng đổi, không đổi khi chỉ sửa nhãn / loại.
func TestLANListV(t *testing.T) {
	a := &LAN{Targets: []LANTarget{{IP: "10.0.0.1", Type: "can", Label: "A"}, {IP: "10.0.0.2", Type: "tn"}}}
	b := &LAN{Targets: []LANTarget{{IP: "10.0.0.1", Type: "khac", Label: "B"}, {IP: "10.0.0.2", Type: "tn"}}}
	c := &LAN{Targets: []LANTarget{{IP: "10.0.0.2", Type: "tn"}, {IP: "10.0.0.1", Type: "can"}}}
	d := &LAN{Targets: []LANTarget{{IP: "10.0.0.1", Type: "can", Kind: "tcp", Port: 80}, {IP: "10.0.0.2", Type: "tn"}}}
	if a.ListV() != b.ListV() {
		t.Error("chỉ đổi nhãn / loại mà ListV đổi")
	}
	if a.ListV() == c.ListV() || a.ListV() == d.ListV() {
		t.Error("đổi thứ tự / kiểu kiểm mà ListV không đổi")
	}
	if len(a.ListV()) != 16 {
		t.Errorf("ListV dài %d", len(a.ListV()))
	}
}

func TestLANDefaultsAndPrefixes(t *testing.T) {
	l := &LAN{Targets: []LANTarget{{IP: "10.1.2.3", Type: "khac"}, {IP: "192.168.5.5", Type: "khac"}}}
	if l.Every() != DefaultLANEveryHB || l.Tries() != 2 || l.Timeout() != DefaultProbeTimeMs {
		t.Fatalf("mặc định sai: %d %d %d", l.Every(), l.Tries(), l.Timeout())
	}
	if (&LAN{Retry: -1}).Tries() != 1 {
		t.Fatal("retry -1 phải là 1 lần thử")
	}
	out := l.OutsidePrefixes([]netip.Prefix{netip.MustParsePrefix("10.1.2.0/24")})
	if len(out) != 1 || out[0] != "192.168.5.5" {
		t.Fatalf("OutsidePrefixes = %v", out)
	}
	if got := l.OutsidePrefixes(nil); len(got) != 2 {
		t.Fatalf("không có dải nào được phép thì mọi đích nằm ngoài, được %v", got)
	}
}

// ForAgent giữ sơ đồ LAN (agent cần).
func TestLANForAgent(t *testing.T) {
	s := lanSet(&LAN{Targets: []LANTarget{{IP: "10.0.0.1", Type: "khac"}}})
	if s.ForAgent().LAN == nil {
		t.Fatal("ForAgent làm mất sơ đồ LAN")
	}
}

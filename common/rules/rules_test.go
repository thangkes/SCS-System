package rules

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sshSet() *RuleSet {
	return &RuleSet{Name: "pos-default", Version: "2026.11.10-01", Rules: []Rule{
		{
			ID: "SSH-FAIL-01", Title: "Đăng nhập SSH sai nhiều lần", Severity: "medium",
			Source:    "/var/log/auth.log",
			Match:     `Failed password for (invalid user )?(?P<user>\S+) from (?P<ip>\S+)`,
			Exclude:   `from 127\.0\.0\.1 `,
			Aggregate: &Aggregate{WindowS: 300, Threshold: 3, GroupBy: "ip"},
			CooldownS: 600,
			Tests: &Tests{
				Match:   []string{"sshd[1]: Failed password for root from 10.0.0.9 port 22 ssh2"},
				NoMatch: []string{"sshd[1]: Accepted password for user from 10.0.0.9 port 22 ssh2", "sshd[1]: Failed password for root from 127.0.0.1 port 22 ssh2"},
			},
		},
		{ID: "USB-ADD-01", Severity: "low", Source: SourceJournald, Unit: "kernel", Match: `New USB device found`},
	}}
}

func TestValidateOK(t *testing.T) {
	if err := sshSet().Validate(); err != nil {
		t.Fatal(err)
	}
	if f := sshSet().RunTests(); len(f) > 0 {
		t.Fatal(f)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]func(r *Rule){
		"id thường":           func(r *Rule) { r.ID = "ssh-fail" },
		"severity lạ":         func(r *Rule) { r.Severity = "urgent" },
		"file ngoài /var/log": func(r *Rule) { r.Source = "/etc/shadow" },
		"có ..":               func(r *Rule) { r.Source = "/var/log/../../etc/shadow" },
		"đường dẫn tương đối": func(r *Rule) { r.Source = "var/log/auth.log" },
		"regex sai":           func(r *Rule) { r.Match = `(abc` },
		"regex khớp rỗng":     func(r *Rule) { r.Match = `.*` },
		"group_by không có":   func(r *Rule) { r.Aggregate.GroupBy = "host" },
		"window 0":            func(r *Rule) { r.Aggregate.WindowS = 0 },
		"unit với file":       func(r *Rule) { r.Unit = "ssh.service" },
		"tên nhóm hoa":        func(r *Rule) { r.Match = `from (?P<IP>\S+)`; r.Aggregate.GroupBy = "" },
		"title điều khiển":    func(r *Rule) { r.Title = "a\nb" },
	}
	for name, mod := range cases {
		s := sshSet()
		mod(&s.Rules[0])
		if s.Validate() == nil {
			t.Errorf("%s: phải báo lỗi", name)
		}
	}
	s := sshSet()
	s.Rules[1].ID = s.Rules[0].ID
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "trùng") {
		t.Errorf("id trùng: %v", err)
	}
}

func TestAggregateGroupCooldown(t *testing.T) {
	e, err := Compile(sshSet())
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	line := func(sec int, ip string) []Event {
		return e.Feed(Line{Source: "/var/log/auth.log", Time: t0.Add(time.Duration(sec) * time.Second),
			Text: "sshd[9]: Failed password for root from " + ip + " port 5 ssh2"})
	}
	if line(0, "10.0.0.1") != nil || line(10, "10.0.0.1") != nil || line(20, "10.0.0.2") != nil {
		t.Fatal("chưa đủ ngưỡng: không được báo")
	}
	ev := line(30, "10.0.0.1")
	if len(ev) != 1 || ev[0].Count != 3 || ev[0].Fields["ip"] != "10.0.0.1" || ev[0].Fields["user"] != "root" || !ev[0].First.Equal(t0) {
		t.Fatalf("lần thứ 3 cùng IP phải báo: %+v", ev)
	}
	// Đang cooldown 600s: dù vượt ngưỡng lần nữa cũng không báo.
	for s := 40; s < 100; s += 10 {
		if line(s, "10.0.0.1") != nil {
			t.Fatal("đang cooldown: không được báo")
		}
	}
	// Cửa sổ trượt: 3 lần cách nhau > 300s không báo.
	if line(1000, "10.0.0.3") != nil || line(1400, "10.0.0.3") != nil || line(1800, "10.0.0.3") != nil {
		t.Fatal("ngoài cửa sổ: không được báo")
	}
	// Hết cooldown thì báo lại.
	line(700, "10.0.0.1")
	line(710, "10.0.0.1")
	if len(line(720, "10.0.0.1")) != 1 {
		t.Fatal("hết cooldown phải báo lại")
	}
	// exclude
	for s := 0; s < 5; s++ {
		if line(2000+s, "127.0.0.1") != nil {
			t.Fatal("127.0.0.1 bị exclude")
		}
	}
}

func TestJournaldUnitAndSources(t *testing.T) {
	e, err := Compile(sshSet())
	if err != nil {
		t.Fatal(err)
	}
	if ev := e.Feed(Line{Source: SourceJournald, Unit: "kernel", Text: "usb 1-1: New USB device found, idVendor=0781"}); len(ev) != 1 {
		t.Fatal("unit kernel phải khớp")
	}
	if ev := e.Feed(Line{Source: SourceJournald, Unit: "cron.service", Text: "New USB device found"}); ev != nil {
		t.Fatal("unit khác: không được khớp")
	}
	if ev := e.Feed(Line{Source: "/var/log/syslog", Text: "New USB device found"}); ev != nil {
		t.Fatal("nguồn khác: không được khớp")
	}
	src := e.Sources()
	if len(src) != 2 || src[0].Source != "/var/log/auth.log" || src[1].Source != SourceJournald || len(src[1].Units) != 1 {
		t.Fatalf("Sources: %+v", src)
	}
}

func TestMaxKeys(t *testing.T) {
	s := sshSet()
	e, _ := Compile(s)
	c := e.bySource["/var/log/auth.log"][0]
	for i := 0; i < MaxKeysPerRule+100; i++ {
		e.Feed(Line{Source: "/var/log/auth.log", Time: time.Unix(0, 0), Text: "Failed password for x from h" + string(rune('a'+i%26)) + strings.Repeat("x", i/26) + " port"})
	}
	if len(c.state) > MaxKeysPerRule {
		t.Fatalf("giữ %d khóa, tối đa %d", len(c.state), MaxKeysPerRule)
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := strings.Repeat("ă", 10) // mỗi ký tự 2 byte
	if got := truncate(s, 5); got != "ăă" {
		t.Fatalf("%q", got)
	}
	if got := truncate("abc", 5); got != "abc" {
		t.Fatal(got)
	}
}

func TestBundle(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s := sshSet()
	s.Rules = append(s.Rules, Rule{ID: "OFF-01", Severity: "info", Source: SourceJournald, Match: "x", Disabled: true})
	b, err := Build(s, priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Open(pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 2 || got.Rules[0].Tests != nil {
		t.Fatalf("bundle phải bỏ tests và rule tắt: %+v", got.Rules)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := b.Open(other); err == nil {
		t.Error("khóa khác: phải bị từ chối")
	}
	// Sửa payload (thêm rule) nhưng giữ chữ ký.
	evil := *got
	evil.Rules = append(evil.Rules, Rule{ID: "EVIL-01", Severity: "info", Source: "/var/log/x", Match: "x"})
	data, _ := json.Marshal(evil)
	bad := *b
	bad.Payload = base64.StdEncoding.EncodeToString(data)
	if _, err := bad.Open(pub); err == nil {
		t.Error("payload bị sửa: phải bị từ chối")
	}
	bad = *b
	bad.Version = "2099.01.01"
	if _, err := bad.Open(pub); err == nil {
		t.Error("đổi phiên bản: phải bị từ chối")
	}
	// Chữ ký bộ rule không dùng được như chữ ký bản agent và ngược lại (khác context).
	if strings.HasPrefix(string(signingInput("a", "b", "c")), "scs-release/") {
		t.Error("context trùng với bản agent")
	}
}

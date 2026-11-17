package watch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/common/rules"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func collect(t *testing.T, ch <-chan rules.Line, n int) []string {
	t.Helper()
	var out []string
	deadline := time.After(5 * time.Second)
	for len(out) < n {
		select {
		case l := <-ch:
			out = append(out, l.Text)
		case <-deadline:
			t.Fatalf("chỉ nhận %d/%d dòng: %v", len(out), n, out)
		}
	}
	return out
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

// Đọc file liên tục: lần đầu bắt đầu từ cuối file, đọc dòng mới, dòng chưa xuống hàng chờ đủ dòng,
// khởi động lại đọc tiếp đúng chỗ, file bị cắt ngắn đọc lại từ đầu, file xoay vòng đọc file mới.
func TestTailer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.log")
	appendFile(t, path, "cu 1\ncu 2\n")
	pos := loadPositions(filepath.Join(dir, "pos.json"))
	out := make(chan rules.Line, 64)
	ctx, cancel := context.WithCancel(context.Background())
	tl := &tailer{path: path, pos: pos, out: out, log: quiet}
	done := make(chan struct{})
	go func() { tl.run(ctx); close(done) }()
	time.Sleep(300 * time.Millisecond)
	appendFile(t, path, "moi 1\nmoi 2 dang ")
	time.Sleep(1500 * time.Millisecond)
	appendFile(t, path, "viet\n")
	if got := collect(t, out, 2); got[0] != "moi 1" || got[1] != "moi 2 dang viet" {
		t.Fatalf("dòng mới: %v", got)
	}
	cancel()
	<-done
	pos.save()

	// Khởi động lại: đọc tiếp, không đọc lại dòng cũ.
	appendFile(t, path, "sau khi tat\n")
	pos2 := loadPositions(filepath.Join(dir, "pos.json"))
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	tl2 := &tailer{path: path, pos: pos2, out: out, log: quiet}
	go tl2.run(ctx)
	if got := collect(t, out, 1); got[0] != "sau khi tat" {
		t.Fatalf("đọc tiếp sau khởi động lại: %v", got)
	}
	// Cắt ngắn (copytruncate): đọc từ đầu.
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(path, []byte("sau cat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, out, 1); got[0] != "sau cat" {
		t.Fatalf("cắt ngắn: %v", got)
	}
	if runtime.GOOS == "windows" {
		return // Windows không có inode: không kiểm xoay vòng kiểu đổi tên
	}
	// Xoay vòng: đổi tên file cũ (có thêm 1 dòng cuối), tạo file mới.
	appendFile(t, path, "dong cuoi file cu\n")
	time.Sleep(100 * time.Millisecond)
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, "file moi\n")
	got := collect(t, out, 2)
	if !strings.Contains(strings.Join(got, "|"), "dong cuoi file cu") || got[len(got)-1] != "file moi" {
		t.Fatalf("xoay vòng: %v", got)
	}
}

func TestParseJournal(t *testing.T) {
	l, cur, ok := parseJournal([]byte(`{"__CURSOR":"s=1","__REALTIME_TIMESTAMP":"1763370000000000","_SYSTEMD_UNIT":"init.scope","MESSAGE":"cups.service: Failed with result 'exit-code'."}`))
	if !ok || cur != "s=1" || l.Unit != "init.scope" || l.Source != rules.SourceJournald || !strings.HasPrefix(l.Text, "cups.service") || l.Time.Year() != 2025 {
		t.Fatalf("%+v %q %v", l, cur, ok)
	}
	l, _, ok = parseJournal([]byte(`{"__CURSOR":"s=2","_TRANSPORT":"kernel","MESSAGE":[104,105,255]}`))
	if !ok || l.Unit != "kernel" || l.Text != "hi?" {
		t.Fatalf("kernel / MESSAGE mảng byte: %+v %v", l, ok)
	}
	if _, cur, ok := parseJournal([]byte(`{"__CURSOR":"s=3"}`)); ok || cur != "s=3" {
		t.Fatal("không có MESSAGE: bỏ dòng nhưng vẫn nhớ con trỏ")
	}
	args := strings.Join(journalArgs([]string{"init.scope", "kernel"}, ""), " ")
	if !strings.Contains(args, "-n 0") || !strings.Contains(args, "_SYSTEMD_UNIT=init.scope + _TRANSPORT=kernel") {
		t.Fatalf("tham số journalctl: %s", args)
	}
	if args := strings.Join(journalArgs(nil, "s=9"), " "); !strings.Contains(args, "--after-cursor=s=9") || strings.Contains(args, "-n 0") {
		t.Fatalf("tham số có con trỏ: %s", args)
	}
}

func TestQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.jsonl")
	q := openQueue(path)
	q.push(protocol.LogEvent{RuleID: "A", Severity: "low", Count: 1})
	q.push(protocol.LogEvent{RuleID: "B", Severity: "critical", Count: 1})
	select {
	case <-q.kick:
	default:
		t.Fatal("sự kiện nặng phải đánh thức sender")
	}
	// Mở lại (khởi động lại agent): còn đủ.
	if q2 := openQueue(path); q2.len() != 2 {
		t.Fatalf("hàng đợi trên đĩa: %d", q2.len())
	}
	q.ack(1)
	if q3 := openQueue(path); q3.len() != 1 {
		t.Fatalf("sau khi gửi: %d", q3.len())
	}
	// Đầy: bỏ sự kiện thường cũ nhất, giữ sự kiện nặng.
	for i := 0; i < maxQueue+10; i++ {
		q.push(protocol.LogEvent{RuleID: "L", Severity: "low", Count: 1})
	}
	batch, hot := q.peek(maxQueue)
	if q.len() != maxQueue || !hot || batch[0].RuleID != "B" {
		t.Fatalf("đầy: len %d hot %v đầu %s", q.len(), hot, batch[0].RuleID)
	}
}

type fakeSender struct {
	mu   sync.Mutex
	fail bool
	got  []protocol.LogEvent
}

func (f *fakeSender) Send(_ context.Context, path, typ, _, _ string, body, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("mất mạng")
	}
	if path != "/v1/logs" || typ != protocol.TypeLogBatch {
		return errors.New("sai đường dẫn")
	}
	f.got = append(f.got, body.(protocol.LogBatchBody).Events...)
	return nil
}

// Sender: sự kiện nặng gửi sau vài giây; mất mạng thì giữ trong hàng đợi.
func TestSender(t *testing.T) {
	fs := &fakeSender{}
	s := &Scanner{Log: quiet, Client: fs, Fingerprint: func() string { return "fp" }, StateDir: t.TempDir()}
	s.q = openQueue(filepath.Join(s.StateDir, "q.jsonl"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.sender(ctx)
	s.q.push(protocol.LogEvent{RuleID: "R", Severity: "high", Count: 1, Time: time.Now()})
	deadline := time.Now().Add(8 * time.Second)
	for {
		fs.mu.Lock()
		n := len(fs.got)
		fs.mu.Unlock()
		if n == 1 && s.q.len() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sự kiện nặng chưa được gửi: gửi %d, còn %d", n, s.q.len())
		}
		time.Sleep(100 * time.Millisecond)
	}
	fs.mu.Lock()
	fs.fail = true
	fs.mu.Unlock()
	s.q.push(protocol.LogEvent{RuleID: "R", Severity: "critical", Count: 1, Time: time.Now()})
	time.Sleep(sendHot + time.Second)
	if s.q.len() != 1 {
		t.Fatalf("mất mạng phải giữ sự kiện: còn %d", s.q.len())
	}
}

func TestToLogEvent(t *testing.T) {
	now := time.Now()
	e := toLogEvent(rules.Event{RuleID: "SVC-01", Severity: "medium", Source: "journald", Unit: "init.scope", Last: now, Count: 0, Sample: "x"})
	if e.Source != "journald:init.scope" || e.Count != 1 || e.Line != "x" || !e.Time.Equal(now.UTC()) {
		t.Fatalf("%+v", e)
	}
}

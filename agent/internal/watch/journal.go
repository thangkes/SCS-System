package watch

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/common/rules"
)

// Đọc systemd journal liên tục bằng "journalctl -f -o json" (không cần cgo / libsystemd). Lọc ngay ở
// journalctl theo unit các rule cần (giảm CPU), nhớ con trỏ (__CURSOR) dòng đã đọc để khởi động lại
// đọc tiếp đúng chỗ. Unit của dòng: _SYSTEMD_UNIT, hoặc "kernel" cho log kernel (_TRANSPORT=kernel).

const unitKernel = "kernel"

type journal struct {
	units      []string // nil = toàn bộ journal
	cursorFile string
	out        chan<- rules.Line
	log        *slog.Logger

	mu     sync.Mutex
	cursor string
}

func journalArgs(units []string, cursor string) []string {
	args := []string{"-f", "-o", "json", "--no-pager", "-q"}
	if cursor != "" {
		args = append(args, "--after-cursor="+cursor)
	} else {
		args = append(args, "-n", "0") // lần đầu: chỉ đọc log mới
	}
	for i, u := range units {
		if i > 0 {
			args = append(args, "+")
		}
		if u == unitKernel {
			args = append(args, "_TRANSPORT=kernel")
		} else {
			args = append(args, "_SYSTEMD_UNIT="+u)
		}
	}
	return args
}

func (j *journal) run(ctx context.Context) {
	bin, err := exec.LookPath("journalctl")
	if err != nil {
		j.log.Warn("không có journalctl: bỏ qua các rule đọc journald", "err", err)
		return
	}
	if b, err := os.ReadFile(j.cursorFile); err == nil && len(b) < 1024 {
		j.cursor = strings.TrimSpace(string(b))
	}
	defer j.saveCursor()
	stopSave := make(chan struct{})
	defer close(stopSave)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopSave:
				return
			case <-t.C:
				j.saveCursor()
			}
		}
	}()
	backoff := 5 * time.Second
	var warnAt time.Time
	restarts := 0
	for ctx.Err() == nil {
		j.mu.Lock()
		cur := j.cursor
		j.mu.Unlock()
		start := time.Now()
		err := j.once(ctx, bin, journalArgs(j.units, cur))
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = 5 * time.Second
		}
		restarts++
		if time.Since(warnAt) > 10*time.Minute { // chết liên tục: báo tối đa mỗi 10 phút, kèm số lần
			j.log.Warn("journalctl dừng, sẽ chạy lại", "err", err, "sau", backoff.String(), "so_lan", restarts)
			warnAt, restarts = time.Now(), 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (j *journal) once(ctx context.Context, bin string, args []string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = []string{"LANG=C.UTF-8", "SYSTEMD_COLORS=0"}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 2*maxLineBytes)
	for sc.Scan() {
		l, cursor, ok := parseJournal(sc.Bytes())
		if cursor != "" {
			j.mu.Lock()
			j.cursor = cursor
			j.mu.Unlock()
		}
		if !ok {
			continue
		}
		select {
		case j.out <- l:
		case <-ctx.Done():
			_ = cmd.Wait()
			return ctx.Err()
		}
	}
	scanErr := sc.Err()
	waitErr := cmd.Wait()
	if scanErr != nil {
		return scanErr
	}
	return waitErr
}

func (j *journal) saveCursor() {
	j.mu.Lock()
	cur := j.cursor
	j.mu.Unlock()
	if cur == "" {
		return
	}
	tmp := j.cursorFile + ".tmp"
	if err := os.MkdirAll(filepath.Dir(j.cursorFile), 0o700); err != nil {
		return
	}
	if os.WriteFile(tmp, []byte(cur+"\n"), 0o600) == nil {
		_ = os.Rename(tmp, j.cursorFile)
	}
}

// parseJournal đọc một dòng JSON của journalctl. MESSAGE có thể là chuỗi hoặc mảng byte (khi không
// phải UTF-8 hợp lệ).
func parseJournal(b []byte) (rules.Line, string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return rules.Line{}, "", false
	}
	str := func(k string) string {
		var s string
		if raw, ok := m[k]; ok && json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	cursor := str("__CURSOR")
	msg := str("MESSAGE")
	if msg == "" {
		var bs []byte
		var ints []int
		if raw, ok := m["MESSAGE"]; ok && json.Unmarshal(raw, &ints) == nil {
			for _, v := range ints {
				bs = append(bs, byte(v))
			}
			msg = strings.ToValidUTF8(string(bs), "?")
		}
	}
	if msg == "" || !utf8.ValidString(msg) {
		return rules.Line{}, cursor, false
	}
	unit := str("_SYSTEMD_UNIT")
	if str("_TRANSPORT") == "kernel" {
		unit = unitKernel
	}
	t := time.Now()
	if us, err := strconv.ParseInt(str("__REALTIME_TIMESTAMP"), 10, 64); err == nil && us > 0 {
		t = time.UnixMicro(us)
	}
	return rules.Line{Source: rules.SourceJournald, Unit: unit, Time: t, Text: msg}, cursor, true
}

package watch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/thangkes/SCS-System/common/rules"
)

// Đọc file log liên tục (kiểu "tail -F"): nhớ vị trí đã đọc theo từng file (positions.json) để khởi
// động lại không đọc lại, nhận ra file bị xoay vòng (logrotate đổi tên, tạo file mới) hay bị cắt ngắn.
// Lần đầu thấy một file thì bắt đầu từ CUỐI file: không quét lại log cũ hàng tháng.

const (
	tailPoll     = time.Second
	tailChunk    = 256 << 10
	maxLineBytes = 1 << 20
)

// position: vị trí đã đọc của một file. ID nhận dạng file (thiết bị + inode trên Linux).
type position struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
}

type tailer struct {
	path  string
	pos   *positions
	out   chan<- rules.Line
	log   *slog.Logger
	f     *os.File
	id    string
	off   int64
	part  []byte
	since time.Time // lần báo lỗi mở file gần nhất (không báo dồn dập)
	// readErrAt: lần báo lỗi đọc file gần nhất (không báo dồn dập).
	readErrAt time.Time
}

func (t *tailer) run(ctx context.Context) {
	defer func() {
		if t.f != nil {
			t.f.Close()
		}
	}()
	tick := time.NewTicker(tailPoll)
	defer tick.Stop()
	for {
		if t.f == nil {
			t.open()
		}
		if t.f != nil {
			if !t.drain(ctx) {
				return
			}
			t.checkRotate(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// open mở file và chọn chỗ bắt đầu đọc.
func (t *tailer) open() {
	f, err := os.Open(t.path)
	if err != nil {
		if time.Since(t.since) > 10*time.Minute {
			if !errors.Is(err, os.ErrNotExist) {
				t.log.Warn("không mở được file log", "file", t.path, "err", err)
			}
			t.since = time.Now()
		}
		return
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return
	}
	if !plainReadable(fi) {
		// Mở được chỉ nhờ quyền đặc biệt của agent (symlink tới file của root…): không đọc (fileid_unix.go).
		f.Close()
		if time.Since(t.since) > 10*time.Minute {
			t.log.Warn("bỏ qua file log: agent không có quyền đọc thường (chủ / nhóm / người khác), có thể là symlink tới file khác",
				"file", t.path)
			t.since = time.Now()
		}
		return
	}
	id := fileID(fi)
	saved, had := t.pos.get(t.path)
	switch {
	case had && saved.ID == id && saved.Offset <= fi.Size():
		t.off = saved.Offset // tiếp tục chỗ đã đọc
	case had:
		t.off = 0 // file đã xoay vòng trong lúc agent tắt: đọc file mới từ đầu
	default:
		t.off = fi.Size() // lần đầu: chỉ đọc log mới
	}
	t.f, t.id, t.part = f, id, nil
	t.pos.set(t.path, position{ID: id, Offset: t.off})
}

// drain đọc hết phần mới của file đang mở, gửi từng dòng. Trả false nếu ctx bị hủy.
func (t *tailer) drain(ctx context.Context) bool {
	buf := make([]byte, tailChunk)
	for {
		n, err := t.f.ReadAt(buf, t.off)
		if n > 0 {
			t.off += int64(n)
			if !t.split(ctx, buf[:n]) {
				return false
			}
			t.pos.set(t.path, position{ID: t.id, Offset: t.off - int64(len(t.part))})
		}
		if err != nil || n < len(buf) {
			if err != nil && !errors.Is(err, io.EOF) {
				// Đọc lỗi (thường là ổ hỏng): mở lại mỗi giây nhưng chỉ báo mỗi 10 phút, không làm đầy journal.
				if time.Since(t.readErrAt) > 10*time.Minute {
					t.log.Warn("lỗi đọc file log (báo tối đa mỗi 10 phút)", "file", t.path, "err", err)
					t.readErrAt = time.Now()
				}
				t.f.Close()
				t.f = nil
			}
			return true
		}
	}
}

func (t *tailer) split(ctx context.Context, b []byte) bool {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if len(t.part) < maxLineBytes {
				t.part = append(t.part, b[:min(len(b), maxLineBytes-len(t.part))]...)
			}
			return true
		}
		line := b[:i]
		if len(t.part) > 0 {
			if len(t.part) < maxLineBytes {
				line = append(t.part, line[:min(len(line), maxLineBytes-len(t.part))]...)
			} else {
				line = t.part
			}
			t.part = nil
		}
		b = b[i+1:]
		line = bytes.TrimSuffix(line, []byte{'\r'})
		select {
		case t.out <- rules.Line{Source: t.path, Time: time.Now(), Text: string(line)}:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

// checkRotate: file ở đường dẫn đã đổi (xoay vòng) hoặc bị cắt ngắn thì đọc nốt file cũ rồi mở lại.
func (t *tailer) checkRotate(ctx context.Context) {
	fi, err := os.Stat(t.path)
	if err != nil {
		return // file tạm biến mất giữa lúc xoay vòng: giữ file cũ, lần sau xem lại
	}
	if fileID(fi) != t.id {
		t.drain(ctx)
		t.f.Close()
		t.f = nil
		t.pos.set(t.path, position{ID: "", Offset: 0}) // file mới: đọc từ đầu
		return
	}
	if fi.Size() < t.off {
		t.off, t.part = 0, nil // bị cắt ngắn (copytruncate)
		t.pos.set(t.path, position{ID: t.id, Offset: 0})
	}
}

package watch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/thangkes/SCS-System/common/protocol"
)

// Hàng đợi sự kiện chờ gửi, lưu trên đĩa (queue.jsonl) để mất mạng hay khởi động lại máy không mất
// sự kiện. Có giới hạn: đầy thì bỏ sự kiện cũ nhất mức thấp trước (info / low / medium), giữ high /
// critical tới cùng.

const maxQueue = 5000

type queue struct {
	mu    sync.Mutex
	path  string
	items []protocol.LogEvent
	kick  chan struct{} // báo sender: có sự kiện nặng, gửi ngay
}

func serious(sev string) bool { return sev == "high" || sev == "critical" }

func openQueue(path string) *queue {
	q := &queue{path: path, kick: make(chan struct{}, 1)}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() && len(q.items) < maxQueue {
			var e protocol.LogEvent
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.RuleID != "" {
				q.items = append(q.items, e)
			}
		}
		f.Close()
	}
	return q
}

func (q *queue) push(e protocol.LogEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= maxQueue {
		drop := 0 // bỏ sự kiện cũ nhất không nặng; toàn sự kiện nặng thì bỏ cái cũ nhất
		for i, x := range q.items {
			if !serious(x.Severity) {
				drop = i
				break
			}
		}
		q.items = append(q.items[:drop], q.items[drop+1:]...)
		q.saveLocked()
	} else if b, err := json.Marshal(e); err == nil {
		q.appendLocked(b)
	}
	q.items = append(q.items, e)
	if len(q.items) >= maxQueue {
		q.saveLocked()
	}
	if serious(e.Severity) {
		select {
		case q.kick <- struct{}{}:
		default:
		}
	}
}

// peek: tối đa n sự kiện đầu hàng đợi (để gửi), và có sự kiện nặng không.
func (q *queue) peek(n int) ([]protocol.LogEvent, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	n = min(n, len(q.items))
	out := append([]protocol.LogEvent(nil), q.items[:n]...)
	hot := false
	for _, e := range q.items {
		if serious(e.Severity) {
			hot = true
			break
		}
	}
	return out, hot
}

// ack: bỏ n sự kiện đầu (đã gửi xong).
func (q *queue) ack(n int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	n = min(n, len(q.items))
	q.items = append([]protocol.LogEvent(nil), q.items[n:]...)
	q.saveLocked()
}

func (q *queue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *queue) appendLocked(b []byte) {
	if err := os.MkdirAll(filepath.Dir(q.path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(q.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	f.Close()
}

func (q *queue) saveLocked() {
	var buf bytes.Buffer
	for _, e := range q.items {
		if b, err := json.Marshal(e); err == nil {
			buf.Write(b)
			buf.WriteByte('\n')
		}
	}
	if err := os.MkdirAll(filepath.Dir(q.path), 0o700); err != nil {
		return
	}
	tmp := q.path + ".tmp"
	if os.WriteFile(tmp, buf.Bytes(), 0o600) == nil {
		_ = os.Rename(tmp, q.path)
	}
}

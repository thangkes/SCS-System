// Package watch: agent quét log liên tục theo bộ rule đang cài (mốc A3).
//
//   - Nạp bộ rule đã ký (scan.Load), dựng Engine theo ngữ cảnh máy (hostname, os, nhãn từ server).
//   - Đọc các nguồn bộ rule cần: file trong /var/log (nhớ vị trí, nhận ra xoay vòng) và journald.
//   - Sự kiện vào hàng đợi trên đĩa; sender gửi lên POST /v1/logs: sự kiện high / critical gửi ngay,
//     còn lại gom gửi mỗi phút. Mất mạng thì giữ lại, gửi khi có mạng.
//   - Bộ rule hay nhãn máy đổi (server phát bộ mới, admin đổi nhãn) thì tự nạp lại trong ~30 giây.
package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thangkes/SCS-System/agent/internal/identity"
	"github.com/thangkes/SCS-System/agent/internal/scan"
	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/common/rules"
)

const (
	recheck     = 30 * time.Second // xem bộ rule / nhãn máy có đổi không
	savePos     = 10 * time.Second
	sendEvery   = time.Minute     // gom sự kiện thường
	sendHot     = 3 * time.Second // sự kiện nặng: chờ chút để gom các dòng đi kèm
	maxBatch    = 500
	sendBackMin = 30 * time.Second
	sendBackMax = 15 * time.Minute
)

// Sender gửi một gói đã ký lên server (client.Client.Send).
type Sender interface {
	Send(ctx context.Context, path, typ, deviceID, fingerprint string, body, out any) error
}

// Scanner: quét log + gửi sự kiện. Active trả SHA-256 bộ rule đang quét ("" = không quét) cho heartbeat.
type Scanner struct {
	Log         *slog.Logger
	StateDir    string
	ReleasePub  string
	Client      Sender
	DeviceID    string
	Fingerprint func() string

	active atomic.Value // string
	q      *queue
	pos    *positions
	wake   chan struct{}
}

// Active: SHA-256 bộ rule đang quét ("" nếu chưa quét).
func (s *Scanner) Active() string {
	v, _ := s.active.Load().(string)
	return v
}

// Reload: bộ rule vừa được cài: nạp lại ngay thay vì chờ lần kiểm kế tiếp.
func (s *Scanner) Reload() {
	if s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scanner) dir() string { return filepath.Join(s.StateDir, "scan") }

// Run quét tới khi ctx kết thúc.
func (s *Scanner) Run(ctx context.Context) {
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	s.active.Store("")
	s.q = openQueue(filepath.Join(s.dir(), "queue.jsonl"))
	s.pos = loadPositions(filepath.Join(s.dir(), "positions.json"))
	go s.sender(ctx)
	defer s.pos.save()

	var stopRun func()
	stop := func() {
		if stopRun != nil {
			stopRun()
			stopRun = nil
		}
		s.active.Store("")
	}
	defer stop()
	lastSig := ""
	t := time.NewTicker(recheck)
	defer t.Stop()
	for {
		sig, set, labels := s.current()
		if sig != lastSig {
			stop()
			lastSig = sig
			if set != nil {
				e, err := rules.CompileFor(set, scan.LocalContext(labels))
				if err != nil {
					s.Log.Warn("bộ rule không có rule nào chạy trên máy này, không quét", "bo", set.Name+"@"+set.Version, "err", err)
				} else {
					stopRun = s.start(ctx, e)
					s.active.Store(bundleSHA(s.StateDir))
					s.Log.Info("bắt đầu quét log theo bộ rule", "bo", set.Name+"@"+set.Version,
						"nguon", len(e.Sources()), "bo_qua", len(e.Skipped()))
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

// start chạy quét với Engine e; hàm trả về dừng quét và chờ các nguồn đóng xong.
func (s *Scanner) start(ctx context.Context, e *rules.Engine) func() {
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); s.scan(rctx, e) }()
	return func() { cancel(); <-done }
}

// current: bộ rule đang cài (đã kiểm chữ ký), nhãn máy, và dấu của cả hai (đổi thì nạp lại).
func (s *Scanner) current() (string, *rules.RuleSet, map[string]string) {
	st, _ := identity.LoadState(s.StateDir)
	set, err := scan.Load(s.StateDir, s.ReleasePub)
	if err != nil {
		if !errors.Is(err, scan.ErrNoBundle) {
			s.Log.Warn("bộ rule đang cài không dùng được, không quét", "err", err)
		}
		return "none", nil, nil
	}
	keys := make([]string, 0, len(st.Labels))
	for k := range st.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte(bundleSHA(s.StateDir)))
	for _, k := range keys {
		h.Write([]byte("\x00" + k + "=" + st.Labels[k]))
	}
	return hex.EncodeToString(h.Sum(nil)), set, st.Labels
}

func bundleSHA(stateDir string) string {
	b, err := os.ReadFile(scan.BundlePath(stateDir))
	if err != nil {
		return ""
	}
	var bd rules.Bundle
	if json.Unmarshal(b, &bd) != nil {
		return ""
	}
	return bd.SHA256
}

// scan: mở các nguồn của Engine, cho từng dòng qua Engine, sự kiện vào hàng đợi.
func (s *Scanner) scan(ctx context.Context, e *rules.Engine) {
	lines := make(chan rules.Line, 256)
	var wg sync.WaitGroup
	for _, sp := range e.Sources() {
		sp := sp
		wg.Add(1)
		go func() {
			defer wg.Done()
			if sp.Source == rules.SourceJournald {
				j := &journal{units: sp.Units, cursorFile: filepath.Join(s.dir(), "journal.cursor"), out: lines, log: s.Log}
				j.run(ctx)
				return
			}
			if !rules.ValidLogPath(sp.Source) {
				return
			}
			t := &tailer{path: sp.Source, pos: s.pos, out: lines, log: s.Log}
			t.run(ctx)
		}()
	}
	tick := time.NewTicker(savePos)
	defer tick.Stop()
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.pos.save()
		case l := <-lines:
			for _, ev := range e.Feed(l) {
				s.q.push(toLogEvent(ev))
			}
		}
	}
}

func toLogEvent(ev rules.Event) protocol.LogEvent {
	src := ev.Source
	if ev.Unit != "" {
		src += ":" + ev.Unit
	}
	if len(src) > 256 {
		src = src[:256]
	}
	return protocol.LogEvent{RuleID: ev.RuleID, Severity: ev.Severity, Source: src, Time: ev.Last.UTC(),
		Line: ev.Sample, Count: max(ev.Count, 1)}
}

// sender gửi hàng đợi lên server: sự kiện nặng sau vài giây, còn lại mỗi phút; lỗi thì lùi dần.
func (s *Scanner) sender(ctx context.Context) {
	t := time.NewTicker(sendEvery)
	defer t.Stop()
	backoff := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.q.kick:
			select { // gom thêm các sự kiện cùng lúc
			case <-ctx.Done():
				return
			case <-time.After(sendHot):
			}
		}
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}
		for s.q.len() > 0 && ctx.Err() == nil {
			batch, _ := s.q.peek(maxBatch)
			var resp protocol.Response
			err := s.Client.Send(ctx, "/v1/logs", protocol.TypeLogBatch, s.DeviceID, s.Fingerprint(),
				protocol.LogBatchBody{Events: batch}, &resp)
			if err != nil {
				if ctx.Err() == nil {
					backoff = min(max(backoff*2, sendBackMin), sendBackMax)
					s.Log.Warn("gửi sự kiện thất bại, giữ lại để gửi sau", "err", err, "dang_cho", s.q.len(),
						"thu_lai_sau", backoff.String())
				}
				break
			}
			backoff = 0
			s.q.ack(len(batch))
		}
	}
}

// positions: vị trí đã đọc của các file, lưu định kỳ.
type positions struct {
	mu    sync.Mutex
	path  string
	m     map[string]position
	dirty bool
}

func loadPositions(path string) *positions {
	p := &positions{path: path, m: map[string]position{}}
	if b, err := os.ReadFile(path); err == nil && len(b) < 1<<20 {
		_ = json.Unmarshal(b, &p.m)
	}
	return p
}

func (p *positions) get(path string) (position, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.m[path]
	return v, ok
}

func (p *positions) set(path string, v position) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m[path] != v {
		p.m[path], p.dirty = v, true
	}
}

func (p *positions) save() {
	p.mu.Lock()
	if !p.dirty {
		p.mu.Unlock()
		return
	}
	b, _ := json.Marshal(p.m)
	p.dirty = false
	p.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return
	}
	tmp := p.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p.path)
	}
}

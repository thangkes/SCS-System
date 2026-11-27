// Package scan: phần agent của khung rule — giữ bộ rule đã ký và cho dòng log đi qua Engine.
//
// Định nghĩa rule, kiểm tra và bộ máy khớp nằm ở common/rules, dùng chung với server, nên
// một rule đã qua "scs-server rules check" sẽ chạy y hệt trên agent.
//
// Đã có: lưu / nạp bộ rule (kiểm chữ ký bằng release.pub), Run đọc dòng từ một Source và báo
// sự kiện. Chưa có (mốc A3): Source đọc file log theo vị trí đã đọc + journald, tải bộ rule
// mới qua heartbeat, hàng đợi sự kiện gửi lên server khi mất mạng.
package scan

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/thangkes/SCS-System/agent/internal/update"
	"github.com/thangkes/SCS-System/common/rules"
)

// ErrNoBundle: máy chưa có bộ rule nào.
var ErrNoBundle = errors.New("chưa có bộ rule")

// BundlePath: nơi lưu bộ rule đang dùng.
func BundlePath(stateDir string) string { return filepath.Join(stateDir, "rules", "current.json") }

// Load nạp bộ rule đang dùng và kiểm chữ ký + nội dung. Dựng Engine cho máy này bằng
// rules.CompileFor(set, LocalContext(...)) để chỉ chạy rule có applies_to khớp.
func Load(stateDir, pubPath string) (*rules.RuleSet, error) {
	data, err := os.ReadFile(BundlePath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoBundle
	}
	if err != nil {
		return nil, err
	}
	return open(data, pubPath)
}

// Install kiểm một bộ rule mới (chữ ký + nội dung) rồi mới thay bộ đang dùng; sai thì giữ bộ cũ.
// Bộ rule không có rule nào khớp ngữ cảnh máy này vẫn cài được (máy khác trong nhóm dùng).
func Install(stateDir, pubPath string, data []byte) (*rules.RuleSet, error) {
	s, err := open(data, pubPath)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(stateDir, BundlePath(stateDir), data); err != nil {
		return nil, err
	}
	return s, nil
}

func open(data []byte, pubPath string) (*rules.RuleSet, error) {
	if len(data) > 2*rules.MaxBundle {
		return nil, errors.New("file bộ rule quá lớn")
	}
	var b rules.Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("file bộ rule hỏng: %w", err)
	}
	pub, err := update.LoadPub(pubPath)
	if err != nil {
		return nil, err
	}
	s, err := b.Open(pub)
	if err != nil {
		return nil, err
	}
	// Bộ phải có ít nhất một rule đang bật (chưa lọc theo máy), hoặc có sơ đồ LAN (kiểm kê thiết bị).
	if _, err := rules.Compile(s); err != nil && !(errors.Is(err, rules.ErrNoRules) && s.LAN != nil) {
		return nil, err
	}
	return s, nil
}

func writeAtomic(stateDir, path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := sameOwner(stateDir, filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rules-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := sameOwner(stateDir, tmp.Name()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Source cung cấp dòng log cho các nguồn bộ rule cần. Đóng kênh khi hết dòng hoặc ctx bị hủy.
// Bản đọc file log thật + journald làm ở mốc A3; hiện có ReaderSource để chạy thử.
type Source interface {
	Lines(ctx context.Context, specs []rules.SourceSpec) (<-chan rules.Line, error)
}

// Run cho mọi dòng từ src đi qua Engine, gọi emit với từng sự kiện. Trả khi kênh đóng hoặc ctx hủy.
func Run(ctx context.Context, e *rules.Engine, src Source, emit func(rules.Event)) error {
	lines, err := src.Lines(ctx, e.Sources())
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case l, ok := <-lines:
			if !ok {
				return nil
			}
			for _, ev := range e.Feed(l) {
				emit(ev)
			}
		}
	}
}

// ReaderSource: đọc dòng từ một io.Reader, coi như đến từ Source/Unit cho trước (thời gian = lúc đọc).
// Dùng cho lệnh "scs-agent rules -log" và test.
type ReaderSource struct {
	R      io.Reader
	Source string
	Unit   string
}

func (r ReaderSource) Lines(ctx context.Context, _ []rules.SourceSpec) (<-chan rules.Line, error) {
	ch := make(chan rules.Line, 64)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(r.R)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			select {
			case ch <- rules.Line{Source: r.Source, Unit: r.Unit, Text: sc.Text()}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

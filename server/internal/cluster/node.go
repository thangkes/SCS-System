// Package cluster: phía server của cụm nhiều SCS-Server (mốc C1, xem docs/thiet-ke-cum-server.md).
//
//   - khóa node Ed25519 (node.key, quyền 600) và chứng chỉ TLS tự ký từ khóa đó;
//   - nhật ký quyết định lưu file chỉ ghi thêm (log.jsonl), kiểm lại toàn bộ khi mở;
//   - cổng 5222: TLS 1.3 hai chiều, chỉ nhận node có khóa trong danh sách cụm.
//
// Phần định dạng, ký và kiểm quyết định nằm ở common/cluster (dùng chung với agent).
package cluster

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
)

// Tên file trong thư mục cụm (SCS_CLUSTER_DIR).
const (
	KeyFile = "node.key"
	LogFile = "log.jsonl"
)

// maxLine: một dòng nhật ký (một quyết định) tối đa.
const maxLine = cc.MaxPayload + 64<<10

// ErrNotInitialized: node chưa vào cụm nào (chưa chạy cluster init / join).
var ErrNotInitialized = errors.New("node chưa thuộc cụm nào (chạy: scs-server cluster init)")

// Node: khóa node + nhật ký quyết định + trạng thái cụm dựng từ nhật ký.
type Node struct {
	Dir  string
	Key  ed25519.PrivateKey
	ID   string
	mu   sync.RWMutex
	st   cc.State
	logF *os.File
	c    consensus
	// local: yêu cầu xin vào cụm, xác nhận của admin, cô lập tại chỗ. joining: node này đang xin vào cụm.
	local   localState
	joining *Joining
}

// LoadOrCreateKey đọc khóa node; chưa có thì sinh mới (quyền 600). Trả khóa và có phải mới tạo.
func LoadOrCreateKey(dir string) (ed25519.PrivateKey, bool, error) {
	path := filepath.Join(dir, KeyFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, false, err
		}
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, false, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return nil, false, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, false, err
		}
		if _, err := f.Write(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
			f.Close()
			return nil, false, err
		}
		if err := f.Close(); err != nil {
			return nil, false, err
		}
		return priv, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
			return nil, false, fmt.Errorf("%s đang quyền %o, phải là 600 (ai đọc được khóa node là giả được node này)", path, st.Mode().Perm())
		}
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, false, fmt.Errorf("%s không phải PEM", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, false, fmt.Errorf("%s: khóa node phải là Ed25519", path)
	}
	return priv, false, nil
}

// Open mở node trong dir: đọc (hoặc sinh) khóa, đọc và kiểm toàn bộ nhật ký.
// Chưa có nhật ký thì vẫn trả Node (Initialized() = false).
func Open(dir string) (*Node, error) {
	priv, _, err := LoadOrCreateKey(dir)
	if err != nil {
		return nil, err
	}
	n := &Node{Dir: dir, Key: priv, ID: cc.NodeID(priv.Public().(ed25519.PublicKey))}
	log, err := readLog(filepath.Join(dir, LogFile))
	if err != nil {
		return nil, err
	}
	if n.st, err = cc.Replay(log); err != nil {
		return nil, fmt.Errorf("nhật ký cụm %s hỏng hoặc bị sửa: %w", filepath.Join(dir, LogFile), err)
	}
	for _, d := range log {
		h, _ := d.Hash()
		n.c.hashes = append(n.c.hashes, h)
	}
	if err := n.loadVotes(); err != nil {
		return nil, fmt.Errorf("đọc phiếu đã bỏ (%s): %w", VotesFile, err)
	}
	if err := n.loadLocal(); err != nil {
		return nil, err
	}
	if n.st.Seq > 0 {
		if _, ok := n.st.Member(n.ID); !ok {
			return nil, fmt.Errorf("khóa node %s không có trong danh sách cụm của nhật ký (khóa bị thay?)", n.ID)
		}
	}
	return n, nil
}

// ReadLog đọc nhật ký quyết định trong thư mục cụm mà không mở node (lệnh chạy tay khi dịch vụ đang
// chạy). Chưa có nhật ký thì trả rỗng. Không kiểm lại chữ ký: dịch vụ đã kiểm trước khi ghi.
func ReadLog(dir string) ([]*cc.Decision, error) { return readLog(filepath.Join(dir, LogFile)) }

func readLog(path string) ([]*cc.Decision, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*cc.Decision
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	line := 0
	for sc.Scan() {
		line++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var d cc.Decision
		if err := json.Unmarshal(sc.Bytes(), &d); err != nil {
			return nil, fmt.Errorf("%s dòng %d: %w", path, line, err)
		}
		out = append(out, &d)
	}
	return out, sc.Err()
}

// Initialized: node đã thuộc một cụm.
func (n *Node) Initialized() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.st.Seq > 0
}

// State: bản sao trạng thái cụm hiện tại.
func (n *Node) State() cc.State {
	n.mu.RLock()
	defer n.mu.RUnlock()
	s := n.st
	s.Members = append([]cc.Member(nil), n.st.Members...)
	return s
}

// Trusted: khóa có phải của một node hợp lệ trong cụm không.
func (n *Node) Trusted(pub ed25519.PublicKey) (cc.Member, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	m, ok := n.st.Trusted(pub)
	if ok && n.locallyIsolated(m.ID) {
		return cc.Member{}, false // admin đã cô lập node này tại chỗ
	}
	return m, ok
}

// Joining: node đang xin vào cụm (chưa có nhật ký).
func (n *Node) Joining() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.joining != nil && n.st.Seq == 0
}

// Init tạo cụm mới với node này là node đầu tiên.
func (n *Node) Init(cluster, name string, addrs cc.Addrs, now time.Time) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.st.Seq > 0 {
		return fmt.Errorf("node đã thuộc cụm %q (mã %s)", n.st.Cluster, short(n.st.ClusterID))
	}
	d, err := cc.Genesis(cluster, name, addrs, n.Key, now)
	if err != nil {
		return err
	}
	return n.appendLocked(d)
}

// Append kiểm rồi ghi một quyết định vào cuối nhật ký (fsync trước khi áp dụng vào bộ nhớ).
func (n *Node) Append(d *cc.Decision) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.appendLocked(d)
}

func (n *Node) appendLocked(d *cc.Decision) error {
	next := n.st
	next.Members = append([]cc.Member(nil), n.st.Members...)
	if err := next.Apply(d); err != nil {
		return err
	}
	line, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if n.logF == nil {
		f, err := os.OpenFile(filepath.Join(n.Dir, LogFile), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		n.logF = f
	}
	if _, err := n.logF.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := n.logF.Sync(); err != nil {
		return err
	}
	n.st = next
	n.c.hashes = append(n.c.hashes, next.Head)
	return nil
}

// Close đóng file nhật ký.
func (n *Node) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.logF == nil {
		return nil
	}
	err := n.logF.Close()
	n.logF = nil
	return err
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

// Short: 16 ký tự đầu (để in mã cụm).
func Short(s string) string { return short(s) }

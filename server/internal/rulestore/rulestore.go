// Package rulestore: kho bộ rule trên server — file nguồn admin đưa lên và bản đã ký.
//
// Thư mục (SCS_RULES_DIR):
//
//	src/     file nguồn .yaml (thêm bằng web, menu, `scs-server rules add`, hoặc chép tay vào)
//	signed/  <tên>@<phiên bản>.json       bộ rule đã ký (gửi cho agent)
//	         <tên>@<phiên bản>.meta.json  ai ký, lúc nào, quyết định cụm số mấy
//
// Trạng thái không lưu riêng mà tính lại mỗi lần đọc, nên chép file vào tay hay sửa file nguồn
// sau khi ký đều được phân loại đúng:
//
//	chua-ky     file nguồn hợp lệ, chưa có bản ký
//	da-ky       có bản ký, khớp đúng nội dung file nguồn và khóa phát hành hiện tại
//	can-ky-lai  có bản ký nhưng file nguồn đã sửa hoặc chữ ký không khớp khóa hiện tại
//	loi         file nguồn sai cú pháp, sai giới hạn, tests không đạt, hoặc trùng phiên bản
package rulestore

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
	"github.com/thangkes/SCS-System/common/rules"
	"github.com/thangkes/SCS-System/server/internal/rulefile"
)

// Trạng thái một bộ rule.
const (
	StatusUnsigned = "chua-ky"
	StatusSigned   = "da-ky"
	StatusStale    = "can-ky-lai"
	StatusInvalid  = "loi"
)

const (
	srcDir    = "src"
	signedDir = "signed"
	// MaxSources: số file nguồn tối đa trong kho (chặn thư mục bị nhét đầy làm chậm trang web).
	MaxSources = 500
)

var (
	// ErrNotFound: không có file / bộ rule đó.
	ErrNotFound = errors.New("không có bộ rule này")
	// ErrConflict: trùng tên + phiên bản với nội dung khác.
	ErrConflict = errors.New("trùng phiên bản")
	fileRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+-]{0,150}\.ya?ml$`)
)

// SignInfo: thông tin lần ký.
type SignInfo struct {
	At         time.Time `json:"at"`
	By         string    `json:"by"`
	SHA256     string    `json:"sha256"`
	SourceFile string    `json:"source_file,omitempty"`
	ClusterSeq uint64    `json:"cluster_seq,omitempty"` // quyết định ruleset.publish của cụm (0 = server đơn)
}

// Approval: cụm đã duyệt một bộ rule (quyết định ruleset.publish).
type Approval struct {
	Seq    uint64    `json:"seq"`
	SHA256 string    `json:"sha256"`
	Admin  string    `json:"admin,omitempty"`
	At     time.Time `json:"at"`
}

// Entry: một bộ rule trong kho.
type Entry struct {
	File        string    `json:"file"` // tên file nguồn trong src/; rỗng = chỉ có bản ký
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Schema      int       `json:"schema,omitempty"`
	Description string    `json:"description,omitempty"`
	Rules       int       `json:"rules"`
	Enabled     int       `json:"enabled"`
	Tests       int       `json:"tests"`
	SHA256      string    `json:"sha256,omitempty"` // phần agent nhận, tính từ file nguồn
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	Size        int64     `json:"size,omitempty"`
	Modified    time.Time `json:"modified,omitempty"`
	Bundle      bool      `json:"bundle"`           // có file bản ký
	Signed      *SignInfo `json:"signed,omitempty"` // thông tin lần ký gần nhất
	Cluster     *Approval `json:"cluster,omitempty"`
}

// Key: tên@phiên bản.
func Key(name, version string) string { return name + "@" + version }

// Store: kho bộ rule trong một thư mục. Pub dùng để kiểm bản đã ký (nil = không kiểm chữ ký).
type Store struct {
	Dir string
	Pub ed25519.PublicKey
	mu  sync.Mutex
}

// Open tạo thư mục kho nếu chưa có (quyền 750).
func Open(dir string, pub ed25519.PublicKey) (*Store, error) {
	if dir == "" {
		return nil, errors.New("chưa khai thư mục rule (SCS_RULES_DIR)")
	}
	for _, d := range []string{dir, filepath.Join(dir, srcDir), filepath.Join(dir, signedDir)} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	return &Store{Dir: dir, Pub: pub}, nil
}

func (s *Store) src(file string) string { return filepath.Join(s.Dir, srcDir, file) }
func (s *Store) bundlePath(name, ver string) string {
	return filepath.Join(s.Dir, signedDir, Key(name, ver)+".json")
}
func (s *Store) metaPath(name, ver string) string {
	return filepath.Join(s.Dir, signedDir, Key(name, ver)+".meta.json")
}

// readRegular đọc file thường (không theo symlink), tối đa max byte.
func readRegular(path string, max int64) ([]byte, os.FileInfo, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, st, errors.New("không phải file thường")
	}
	if st.Size() > max {
		return nil, st, fmt.Errorf("file %d byte, tối đa %d", st.Size(), max)
	}
	b, err := os.ReadFile(path)
	return b, st, err
}

// writeAtomic ghi file tạm rồi đổi tên: không bao giờ để file dở dang.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Parse đọc, kiểm và chạy tests một nội dung YAML; trả bộ rule, SHA-256 phần agent nhận và số liệu.
func Parse(data []byte) (*rules.RuleSet, string, Entry, error) {
	var e Entry
	set, err := rulefile.Parse(data)
	if err != nil {
		return nil, "", e, err
	}
	e.Name, e.Version, e.Schema, e.Description = set.Name, set.Version, set.Schema, set.Description
	for _, r := range set.Rules {
		e.Rules++
		if !r.Disabled {
			e.Enabled++
		}
		if r.Tests != nil {
			e.Tests += len(r.Tests.Match) + len(r.Tests.NoMatch)
		}
	}
	if fails := set.RunTests(); len(fails) > 0 {
		return set, "", e, fmt.Errorf("tests không đạt:\n%s", strings.Join(fails, "\n"))
	}
	sha, err := rules.Digest(set)
	if err != nil {
		return set, "", e, err
	}
	e.SHA256 = sha
	return set, sha, e, nil
}

// bundleState: đọc bản ký của name@ver; trả (có file, SHA trong bản ký, lỗi chữ ký).
func (s *Store) bundleState(name, ver string) (bool, string, error) {
	b, _, err := readRegular(s.bundlePath(name, ver), rules.MaxBundle*2)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return true, "", err
	}
	var bd rules.Bundle
	if err := json.Unmarshal(b, &bd); err != nil {
		return true, "", fmt.Errorf("bản ký hỏng: %w", err)
	}
	if bd.Name != name || bd.Version != ver {
		return true, bd.SHA256, errors.New("bản ký mang tên / phiên bản khác tên file")
	}
	if s.Pub != nil {
		if _, err := bd.Open(s.Pub); err != nil {
			return true, bd.SHA256, fmt.Errorf("%w (khóa phát hành đã đổi?)", err)
		}
	}
	return true, bd.SHA256, nil
}

func (s *Store) meta(name, ver string) *SignInfo {
	b, _, err := readRegular(s.metaPath(name, ver), 64<<10)
	if err != nil {
		return nil
	}
	var m SignInfo
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return &m
}

// List: mọi bộ rule trong kho kèm trạng thái, xếp theo tên rồi phiên bản mới trước.
func (s *Store) List() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list()
}

func (s *Store) list() ([]Entry, error) {
	des, err := os.ReadDir(filepath.Join(s.Dir, srcDir))
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		name := de.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		if len(out) >= MaxSources {
			break
		}
		e := Entry{File: name}
		if !fileRe.MatchString(name) {
			e.Status, e.Error = StatusInvalid, "tên file phải là chữ, số, . _ - @ + và đuôi .yaml"
			out = append(out, e)
			continue
		}
		data, st, err := readRegular(s.src(name), rulefile.MaxFile)
		if st != nil {
			e.Size, e.Modified = st.Size(), st.ModTime()
		}
		if err != nil {
			e.Status, e.Error = StatusInvalid, err.Error()
			out = append(out, e)
			continue
		}
		_, _, info, perr := Parse(data)
		info.File, info.Size, info.Modified = e.File, e.Size, e.Modified
		e = info
		if perr != nil {
			e.Status, e.Error = StatusInvalid, perr.Error()
		}
		out = append(out, e)
	}

	// Hai file nguồn cùng tên + phiên bản nhưng khác nội dung: không biết bản nào đúng.
	shaOf := map[string]map[string]string{}
	for _, e := range out {
		if e.SHA256 == "" {
			continue
		}
		k := Key(e.Name, e.Version)
		if shaOf[k] == nil {
			shaOf[k] = map[string]string{}
		}
		shaOf[k][e.SHA256] = e.File
	}
	seen := map[string]bool{}
	for i := range out {
		e := &out[i]
		if e.Name == "" {
			continue
		}
		k := Key(e.Name, e.Version)
		seen[k] = true
		if e.Status == StatusInvalid {
			continue
		}
		if len(shaOf[k]) > 1 {
			e.Status, e.Error = StatusInvalid, "có file nguồn khác cùng tên và phiên bản nhưng nội dung khác: đổi version ở một file"
			continue
		}
		s.classify(e)
	}

	// Bản ký không còn file nguồn (đã xóa nguồn, hoặc chép bản ký vào tay).
	sdes, err := os.ReadDir(filepath.Join(s.Dir, signedDir))
	if err != nil {
		return nil, err
	}
	for _, de := range sdes {
		n := de.Name()
		if !strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".meta.json") {
			continue
		}
		base := strings.TrimSuffix(n, ".json")
		at := strings.LastIndex(base, "@")
		if at <= 0 || seen[base] {
			continue
		}
		e := Entry{Name: base[:at], Version: base[at+1:]}
		has, sha, berr := s.bundleState(e.Name, e.Version)
		e.Bundle, e.SHA256, e.Signed = has, sha, s.meta(e.Name, e.Version)
		e.Status = StatusSigned
		if berr != nil {
			e.Status, e.Error = StatusStale, berr.Error()
		}
		out = append(out, e)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Version != out[j].Version {
			return out[i].Version > out[j].Version
		}
		return out[i].File < out[j].File
	})
	return out, nil
}

// classify: so file nguồn hợp lệ với bản ký.
func (s *Store) classify(e *Entry) {
	has, sha, err := s.bundleState(e.Name, e.Version)
	e.Bundle, e.Signed = has, s.meta(e.Name, e.Version)
	switch {
	case !has:
		e.Status = StatusUnsigned
	case err != nil:
		e.Status, e.Error = StatusStale, err.Error()
	case sha != e.SHA256:
		e.Status, e.Error = StatusStale, "file nguồn đã sửa sau khi ký (nội dung khác bản ký): đổi version rồi duyệt lại"
	default:
		e.Status = StatusSigned
	}
}

// Find tìm bộ rule theo tên file nguồn.
func (s *Store) Find(file string) (Entry, error) {
	list, err := s.List()
	if err != nil {
		return Entry{}, err
	}
	for _, e := range list {
		if e.File != "" && e.File == file {
			return e, nil
		}
	}
	return Entry{}, ErrNotFound
}

// Conflict: hàm kiểm thêm khi thêm / ký (ví dụ cụm đã duyệt name@version với SHA khác). nil = bỏ qua.
type Conflict func(name, version, sha string) error

// Add thêm (hoặc thay) file nguồn từ nội dung YAML. Bộ rule phải hợp lệ và đạt tests. File lưu
// tên <tên>@<phiên bản>.yaml. Không cho thêm nếu cùng tên + phiên bản đã có nội dung khác (đã ký,
// hoặc file nguồn khác): muốn sửa thì tăng version.
func (s *Store) Add(data []byte, conflict Conflict) (Entry, error) {
	_, sha, info, err := Parse(data)
	if err != nil {
		return info, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.list()
	if err != nil {
		return info, err
	}
	file := Key(info.Name, info.Version) + ".yaml"
	srcCount := 0
	for _, e := range list {
		if e.File != "" {
			srcCount++
		}
		if e.Name != info.Name || e.Version != info.Version {
			continue
		}
		if e.Bundle && e.Status != StatusInvalid {
			_, bsha, berr := s.bundleState(e.Name, e.Version)
			if berr == nil && bsha != sha {
				return info, fmt.Errorf("%w: %s đã ký với nội dung khác; đổi version (ví dụ thêm -02) rồi thêm lại", ErrConflict, Key(info.Name, info.Version))
			}
		}
		if e.File != "" && e.File != file && e.SHA256 != "" && e.SHA256 != sha {
			return info, fmt.Errorf("%w: file %s đã có %s với nội dung khác", ErrConflict, e.File, Key(info.Name, info.Version))
		}
	}
	if _, err := os.Lstat(s.src(file)); errors.Is(err, os.ErrNotExist) && srcCount >= MaxSources {
		return info, fmt.Errorf("kho đã có %d file nguồn, xóa bớt bộ cũ trước", MaxSources)
	}
	if conflict != nil {
		if err := conflict(info.Name, info.Version, sha); err != nil {
			return info, err
		}
	}
	if err := writeAtomic(s.src(file), data); err != nil {
		return info, err
	}
	info.File = file
	s.classify(&info)
	return info, nil
}

// Prepare đọc lại file nguồn và kiểm (dùng trước khi đưa đề nghị vào cụm và trước khi ký).
func (s *Store) Prepare(file string) (*rules.RuleSet, string, error) {
	if !fileRe.MatchString(file) {
		return nil, "", ErrNotFound
	}
	data, _, err := readRegular(s.src(file), rulefile.MaxFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	set, sha, _, err := Parse(data)
	return set, sha, err
}

// Sign ký file nguồn bằng khóa phát hành, ghi bản ký và thông tin lần ký. wantSHA khác rỗng thì
// nội dung phải đúng SHA đó (đã được cụm duyệt). Ký lại cùng nội dung thì chỉ cập nhật thông tin.
func (s *Store) Sign(file string, priv ed25519.PrivateKey, by string, clusterSeq uint64, wantSHA string) (Entry, error) {
	if priv == nil {
		return Entry{}, errors.New("server chưa có khóa phát hành (SCS_RELEASE_KEY): không ký được")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.list()
	if err != nil {
		return Entry{}, err
	}
	var cur *Entry
	for i := range list {
		if list[i].File == file {
			cur = &list[i]
		}
	}
	if cur == nil {
		return Entry{}, ErrNotFound
	}
	if cur.Status == StatusInvalid {
		return *cur, fmt.Errorf("bộ rule đang lỗi, không ký: %s", cur.Error)
	}
	data, _, err := readRegular(s.src(file), rulefile.MaxFile)
	if err != nil {
		return *cur, err
	}
	set, sha, _, err := Parse(data)
	if err != nil {
		return *cur, err
	}
	if wantSHA != "" && sha != wantSHA {
		return *cur, errors.New("file nguồn đã đổi so với nội dung cụm vừa duyệt: không ký")
	}
	if has, bsha, berr := s.bundleState(set.Name, set.Version); has && berr == nil && bsha != sha {
		return *cur, fmt.Errorf("%w: %s đã ký với nội dung khác; đổi version rồi duyệt lại", ErrConflict, Key(set.Name, set.Version))
	}
	b, err := rules.Build(set, priv)
	if err != nil {
		return *cur, err
	}
	out, _ := json.MarshalIndent(b, "", "  ")
	if err := writeAtomic(s.bundlePath(set.Name, set.Version), append(out, '\n')); err != nil {
		return *cur, err
	}
	m := SignInfo{At: time.Now().UTC(), By: by, SHA256: b.SHA256, SourceFile: file, ClusterSeq: clusterSeq}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := writeAtomic(s.metaPath(set.Name, set.Version), append(mb, '\n')); err != nil {
		return *cur, err
	}
	s.classify(cur)
	return *cur, nil
}

// Remove xóa file nguồn; withBundle thì xóa luôn bản ký của bộ đó.
func (s *Store) Remove(file string, withBundle bool) error {
	if !fileRe.MatchString(file) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var name, ver string
	if data, _, err := readRegular(s.src(file), rulefile.MaxFile); err == nil {
		if set, perr := rulefile.Parse(data); perr == nil {
			name, ver = set.Name, set.Version
		}
	}
	if err := os.Remove(s.src(file)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	if withBundle && name != "" {
		_ = os.Remove(s.bundlePath(name, ver))
		_ = os.Remove(s.metaPath(name, ver))
	}
	return nil
}

// RemoveBundle xóa bản ký name@ver (khi không còn file nguồn).
func (s *Store) RemoveBundle(name, ver string) error {
	if !validKey(name, ver) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.bundlePath(name, ver)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	_ = os.Remove(s.metaPath(name, ver))
	return nil
}

// Source trả nội dung file nguồn.
func (s *Store) Source(file string) ([]byte, error) {
	if !fileRe.MatchString(file) {
		return nil, ErrNotFound
	}
	b, _, err := readRegular(s.src(file), rulefile.MaxFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// Bundle trả bộ rule đã ký name@ver (JSON gửi agent).
func (s *Store) Bundle(name, ver string) ([]byte, error) {
	if !validKey(name, ver) {
		return nil, ErrNotFound
	}
	b, _, err := readRegular(s.bundlePath(name, ver), rules.MaxBundle*2)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var verRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

func validKey(name, ver string) bool {
	return nameRe.MatchString(name) && verRe.MatchString(ver) && !strings.Contains(ver, "..")
}

// Approvals: các bộ rule cụm đã duyệt (quyết định ruleset.publish) theo tên@phiên bản. Một phiên
// bản được duyệt nhiều lần thì giữ lần đầu: lần sau khác SHA là bất thường (server đã chặn).
func Approvals(log []*cc.Decision) map[string]Approval {
	out := map[string]Approval{}
	for _, d := range log {
		if d.Type != cc.TypeRulesetPublish {
			continue
		}
		var p cc.RulesetPayload
		dec := json.NewDecoder(bytes.NewReader(d.Payload))
		dec.DisallowUnknownFields()
		if dec.Decode(&p) != nil || p.Validate() != nil {
			continue
		}
		k := Key(p.Name, p.Version)
		if _, ok := out[k]; !ok {
			out[k] = Approval{Seq: d.Seq, SHA256: p.SHA256, Admin: d.Admin, At: d.Created}
		}
	}
	return out
}

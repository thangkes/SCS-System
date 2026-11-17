// Package addrbook giữ danh sách địa chỉ server gửi cho agent (IP LAN, IP WAN, tên miền DDNS, IP VPN).
//
// Danh sách nằm trong một file JSON (SCS_AGENT_SERVERS_FILE), sửa được lúc server đang chạy từ web
// quản trị hoặc lệnh `scs-server servers`, không phải khởi động lại và không phải cấp lại chứng chỉ
// (agent không so IP trong chứng chỉ server). Server gửi danh sách cho agent mỗi heartbeat; agent lưu
// lại và tự chuyển sang địa chỉ kế tiếp khi địa chỉ đang dùng không gọi được.
//
// Để đổi địa điểm / đổi mạng không làm mất kết nối hàng loạt, Book còn đếm (trong RAM, theo heartbeat)
// bao nhiêu máy đã nhận danh sách mới nhất và bao nhiêu máy còn gọi qua từng địa chỉ:
// thêm địa chỉ mới → chờ gần đủ máy nhận → chuyển mạng → địa chỉ cũ hết máy dùng thì gỡ.
package addrbook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/common/protocol"
)

// MaxNote: độ dài tối đa ghi chú của một địa chỉ (ví dụ "Kho mới - VNPT").
const MaxNote = 80

// maxFile: file danh sách lớn hơn thì coi là hỏng (8 địa chỉ chỉ vài KB).
const maxFile = 64 << 10

// recheck: Book xem file có bị sửa từ ngoài (lệnh `scs-server servers`) không, tối đa mỗi chừng này.
const recheck = 5 * time.Second

var (
	ErrDuplicate = errors.New("địa chỉ đã có trong danh sách")
	ErrNotFound  = errors.New("không có địa chỉ này trong danh sách")
	ErrFull      = fmt.Errorf("danh sách đã đủ %d địa chỉ", protocol.MaxServers)
	ErrLast      = errors.New("không gỡ được địa chỉ cuối cùng: agent sẽ không còn địa chỉ nào để gọi về")
	ErrBadNote   = fmt.Errorf("ghi chú dài quá %d ký tự hoặc có ký tự điều khiển", MaxNote)
)

// Entry: một địa chỉ trong danh sách.
type Entry struct {
	URL   string    `json:"url"`
	Note  string    `json:"note,omitempty"`
	Added time.Time `json:"added,omitzero"`
}

type fileData struct {
	Entries []Entry   `json:"entries"`
	Updated time.Time `json:"updated,omitzero"`
}

// Book: danh sách địa chỉ + số liệu agent báo về. Dùng chung cho cổng agent và web quản trị
// (cùng tiến trình `scs-server all`).
type Book struct {
	path string

	mu        sync.Mutex
	entries   []Entry
	updated   time.Time
	mod       time.Time // ModTime của file lúc đọc lần cuối
	size      int64
	checked   time.Time
	reloadErr error

	started time.Time
	seen    map[int64]report
	now     func() time.Time
}

type report struct {
	hash string
	url  string
	at   time.Time
}

// Open đọc file danh sách. Chưa có file thì tạo từ seed (SCS_AGENT_SERVERS cũ trong server.env) để
// máy đang chạy giữ nguyên danh sách khi lên bản này; seed rỗng thì danh sách rỗng (agent chỉ dùng
// địa chỉ lúc cài) và chưa ghi file.
func Open(path string, seed []string) (*Book, error) {
	b := &Book{path: path, started: time.Now(), seen: map[int64]report{}, now: time.Now}
	err := b.load()
	if errors.Is(err, os.ErrNotExist) {
		list := protocol.CleanServers(seed)
		if len(list) == 0 {
			return b, nil
		}
		now := b.now().UTC()
		for _, u := range list {
			b.entries = append(b.entries, Entry{URL: u, Note: "từ SCS_AGENT_SERVERS", Added: now})
		}
		return b, b.saveLocked()
	}
	if err != nil {
		// File hỏng: server vẫn chạy (agent vẫn heartbeat được), tạm gửi danh sách seed, web báo lỗi
		// và không cho sửa tới khi file được sửa lại (`scs-server servers set` ghi đè được).
		for _, u := range protocol.CleanServers(seed) {
			b.entries = append(b.entries, Entry{URL: u})
		}
		b.reloadErr, b.checked = err, b.now()
		return b, err
	}
	return b, nil
}

// Set thay cả danh sách (lệnh `scs-server servers set`, install.sh --agent-servers). Ghi đè được
// cả file hỏng. Ghi chú của địa chỉ đã có được giữ lại.
func (b *Book) Set(list []string) error {
	var in []Entry
	for _, u := range list {
		if u = normURL(u); u != "" {
			in = append(in, Entry{URL: u})
		}
	}
	entries, err := clean(in)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return ErrLast
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().UTC()
	for i := range entries {
		entries[i].Added = now
		for _, old := range b.entries {
			if old.URL == entries[i].URL {
				entries[i].Note, entries[i].Added = old.Note, old.Added
			}
		}
	}
	prev, prevErr := b.entries, b.reloadErr
	b.entries, b.reloadErr = entries, nil
	if err := b.saveLocked(); err != nil {
		b.entries, b.reloadErr = prev, prevErr
		return err
	}
	return nil
}

// Path: file đang dùng.
func (b *Book) Path() string { return b.path }

func (b *Book) load() error {
	fi, err := os.Stat(b.path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxFile {
		return fmt.Errorf("%s không phải file danh sách hợp lệ", b.path)
	}
	raw, err := os.ReadFile(b.path)
	if err != nil {
		return err
	}
	var f fileData
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("%s hỏng: %w", b.path, err)
	}
	entries, err := clean(f.Entries)
	if err != nil {
		return fmt.Errorf("%s: %w", b.path, err)
	}
	b.entries, b.updated, b.mod, b.size, b.checked = entries, f.Updated, fi.ModTime(), fi.Size(), b.now()
	return nil
}

// clean kiểm từng địa chỉ: sai dạng / trùng / quá số lượng thì báo lỗi (không âm thầm bỏ).
func clean(in []Entry) ([]Entry, error) {
	out := make([]Entry, 0, len(in))
	seen := map[string]bool{}
	for _, e := range in {
		e.URL = normURL(e.URL)
		if err := protocol.CheckServerURL(e.URL); err != nil {
			return nil, err
		}
		if seen[e.URL] {
			return nil, fmt.Errorf("%s: %w", e.URL, ErrDuplicate)
		}
		if !cleanNote(e.Note) {
			return nil, fmt.Errorf("%s: %w", e.URL, ErrBadNote)
		}
		seen[e.URL] = true
		out = append(out, e)
	}
	if len(out) > protocol.MaxServers {
		return nil, ErrFull
	}
	return out, nil
}

func normURL(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

func cleanNote(s string) bool {
	if utf8.RuneCountInString(s) > MaxNote || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Co, unicode.Cs) {
			return false
		}
	}
	return true
}

// refreshLocked đọc lại file nếu đã bị sửa từ ngoài. File hỏng thì giữ danh sách đang có.
func (b *Book) refreshLocked() {
	if b.now().Sub(b.checked) < recheck {
		return
	}
	b.checked = b.now()
	fi, err := os.Stat(b.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && len(b.entries) == 0 {
			b.reloadErr = nil
			return
		}
		b.reloadErr = err
		return
	}
	if fi.ModTime().Equal(b.mod) && fi.Size() == b.size {
		return
	}
	old := slices.Clone(b.entries)
	if err := b.load(); err != nil {
		b.entries, b.reloadErr = old, err
		return
	}
	b.reloadErr = nil
}

// saveLocked ghi file kiểu nguyên tử (file tạm rồi đổi tên), quyền 640.
func (b *Book) saveLocked() error {
	b.updated = b.now().UTC()
	raw, err := json.MarshalIndent(fileData{Entries: b.entries, Updated: b.updated}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(b.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agent-servers-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
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
	if err := os.Rename(tmp.Name(), b.path); err != nil {
		return err
	}
	if fi, err := os.Stat(b.path); err == nil {
		b.mod, b.size, b.checked = fi.ModTime(), fi.Size(), b.now()
	}
	return nil
}

// URLs: danh sách gửi cho agent, theo thứ tự ưu tiên.
func (b *Book) URLs() []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refreshLocked()
	return urls(b.entries)
}

func urls(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.URL
	}
	return out
}

// Entries: bản sao danh sách kèm ghi chú.
func (b *Book) Entries() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refreshLocked()
	return slices.Clone(b.entries)
}

// Add thêm địa chỉ vào cuối danh sách (đầu danh sách nếu first).
func (b *Book) Add(url, note string, first bool) (Entry, error) {
	url = normURL(url)
	if err := protocol.CheckServerURL(url); err != nil {
		return Entry{}, err
	}
	note = strings.TrimSpace(note)
	if !cleanNote(note) {
		return Entry{}, ErrBadNote
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checked = time.Time{}
	b.refreshLocked()
	if b.reloadErr != nil {
		return Entry{}, fmt.Errorf("không đọc được file danh sách, chưa sửa: %w", b.reloadErr)
	}
	for _, e := range b.entries {
		if e.URL == url {
			return Entry{}, ErrDuplicate
		}
	}
	if len(b.entries) >= protocol.MaxServers {
		return Entry{}, ErrFull
	}
	e := Entry{URL: url, Note: note, Added: b.now().UTC()}
	old := slices.Clone(b.entries)
	if first {
		b.entries = append([]Entry{e}, b.entries...)
	} else {
		b.entries = append(b.entries, e)
	}
	if err := b.saveLocked(); err != nil {
		b.entries = old
		return Entry{}, err
	}
	return e, nil
}

// Remove gỡ một địa chỉ. Không gỡ được địa chỉ cuối cùng.
func (b *Book) Remove(url string) error {
	url = normURL(url)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checked = time.Time{}
	b.refreshLocked()
	if b.reloadErr != nil {
		return fmt.Errorf("không đọc được file danh sách, chưa sửa: %w", b.reloadErr)
	}
	i := slices.IndexFunc(b.entries, func(e Entry) bool { return e.URL == url })
	if i < 0 {
		return ErrNotFound
	}
	if len(b.entries) == 1 {
		return ErrLast
	}
	old := slices.Clone(b.entries)
	b.entries = slices.Delete(b.entries, i, i+1)
	if err := b.saveLocked(); err != nil {
		b.entries = old
		return err
	}
	return nil
}

// Move đổi vị trí ưu tiên: delta -1 = lên một bậc, +1 = xuống một bậc.
func (b *Book) Move(url string, delta int) error {
	url = normURL(url)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checked = time.Time{}
	b.refreshLocked()
	if b.reloadErr != nil {
		return fmt.Errorf("không đọc được file danh sách, chưa sửa: %w", b.reloadErr)
	}
	i := slices.IndexFunc(b.entries, func(e Entry) bool { return e.URL == url })
	if i < 0 {
		return ErrNotFound
	}
	j := i + delta
	if delta == 0 || j < 0 || j >= len(b.entries) {
		return nil
	}
	old := slices.Clone(b.entries)
	b.entries[i], b.entries[j] = b.entries[j], b.entries[i]
	if err := b.saveLocked(); err != nil {
		b.entries = old
		return err
	}
	return nil
}

// Report ghi lại điều agent báo trong heartbeat (dấu danh sách đang giữ, địa chỉ vừa dùng).
// Chỉ nhận địa chỉ đúng dạng để web không hiện chuỗi lạ do agent gửi.
func (b *Book) Report(device int64, hash, url string) {
	if b == nil {
		return
	}
	url = normURL(url)
	if protocol.CheckServerURL(url) != nil || len(url) > 256 {
		url = ""
	}
	if len(hash) > 32 {
		hash = ""
	}
	b.mu.Lock()
	b.seen[device] = report{hash: hash, url: url, at: b.now()}
	b.mu.Unlock()
}

// Forget bỏ số liệu của một máy (máy bị thu hồi).
func (b *Book) Forget(device int64) {
	b.mu.Lock()
	delete(b.seen, device)
	b.mu.Unlock()
}

// Window: chỉ tính các máy đã gọi heartbeat trong khoảng này (heartbeat 5–10 phút: ít nhất 2 nhịp).
const Window = 20 * time.Minute

// AddrStat: số máy gọi qua một địa chỉ.
type AddrStat struct {
	URL    string `json:"url"`
	Note   string `json:"note,omitempty"`
	Added  string `json:"added,omitempty"`
	InList bool   `json:"in_list"` // false: địa chỉ đã gỡ khỏi danh sách hoặc địa chỉ lúc cài
	Using  int    `json:"using"`
}

// Stats: số liệu cho trang web.
type Stats struct {
	Hash     string     `json:"hash"`
	Updated  time.Time  `json:"updated,omitzero"`
	File     string     `json:"file"`
	FileErr  string     `json:"file_error,omitempty"`
	Since    time.Time  `json:"since"`   // số liệu tính từ lúc server khởi động
	Online   int        `json:"online"`  // máy gọi heartbeat trong Window
	Synced   int        `json:"synced"`  // trong đó đã giữ đúng danh sách hiện tại
	Unknown  int        `json:"unknown"` // agent bản cũ, không báo dấu danh sách
	Pending  []int64    `json:"pending"` // máy chưa nhận danh sách mới (tối đa 200)
	Addrs    []AddrStat `json:"addrs"`   // danh sách hiện tại, rồi tới các địa chỉ ngoài danh sách
	Max      int        `json:"max"`
	WindowS  int        `json:"window_s"`
	Previous int        `json:"previous"` // máy còn giữ danh sách cũ
}

// Snapshot tính số liệu hiện tại.
func (b *Book) Snapshot() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refreshLocked()
	list := urls(b.entries)
	st := Stats{Hash: protocol.ServersHash(list), Updated: b.updated, File: b.path, Since: b.started,
		Pending: []int64{}, Max: protocol.MaxServers, WindowS: int(Window / time.Second)}
	if b.reloadErr != nil {
		st.FileErr = b.reloadErr.Error()
	}
	using := map[string]int{}
	cut := b.now().Add(-Window)
	for id, r := range b.seen {
		if r.at.Before(cut) {
			delete(b.seen, id) // máy lâu không gọi: không giữ mãi trong RAM
			continue
		}
		st.Online++
		switch {
		case r.hash == "":
			st.Unknown++
		case r.hash == st.Hash:
			st.Synced++
		default:
			st.Previous++
			if len(st.Pending) < 200 {
				st.Pending = append(st.Pending, id)
			}
		}
		if r.url != "" {
			using[r.url]++
		}
	}
	slices.Sort(st.Pending)
	for _, e := range b.entries {
		a := AddrStat{URL: e.URL, Note: e.Note, InList: true, Using: using[e.URL]}
		if !e.Added.IsZero() {
			a.Added = e.Added.Format(time.RFC3339)
		}
		st.Addrs = append(st.Addrs, a)
		delete(using, e.URL)
	}
	var extra []AddrStat
	for u, n := range using {
		extra = append(extra, AddrStat{URL: u, Using: n})
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].Using > extra[j].Using })
	st.Addrs = append(st.Addrs, extra...)
	if st.Addrs == nil {
		st.Addrs = []AddrStat{}
	}
	return st
}

// UsingCount: số máy (trong Window) đang gọi qua url.
func (b *Book) UsingCount(url string) int {
	url = normURL(url)
	b.mu.Lock()
	defer b.mu.Unlock()
	n, cut := 0, b.now().Add(-Window)
	for _, r := range b.seen {
		if r.url == url && !r.at.Before(cut) {
			n++
		}
	}
	return n
}

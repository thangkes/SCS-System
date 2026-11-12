// Package cluster: phần dùng chung của cụm nhiều SCS-Server (xem docs/thiet-ke-cum-server.md).
//
// Cụm không có CA: mỗi node có khóa Ed25519 riêng; danh sách cụm (node nào, khóa nào, trạng
// thái gì) được dựng lại từ nhật ký quyết định. Mỗi quyết định nối với quyết định trước bằng
// mã băm và phải có đủ chữ ký của các node theo quy tắc của loại quyết định đó (quá nửa node
// hợp lệ; thêm node: 100%). Node và agent đều tự kiểm chữ ký, không tin node đưa dữ liệu.
//
// Package này chỉ có phần thuần (định dạng, ký, kiểm, áp dụng), không có mạng hay file, để
// server và agent dùng chung một cách kiểm.
package cluster

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

// sigContext: ngữ cảnh ký riêng của cụm, để chữ ký quyết định không dùng thay được cho chữ ký
// heartbeat, bộ rule hay bản agent (và ngược lại).
const sigContext = "scs-cluster/v1\n"

// Giới hạn.
const (
	MaxPayload   = 64 << 10
	MaxMembers   = 64
	MaxAddrs     = 8
	MaxSigs      = MaxMembers
	MaxClockSkew = 5 * time.Minute
)

// Loại quyết định.
const (
	TypeGenesis   = "member.genesis" // tạo cụm (1 node, tự ký)
	TypeMemberAdd = "member.add"     // thêm node: 100% node hợp lệ + node mới ký
)

// appTypes: quyết định của ứng dụng (duyệt máy, cấu hình…): quá nửa node hợp lệ ký. Nội dung do
// server kiểm và áp vào DB; phần chung chỉ kiểm chữ ký và thứ tự.
var appTypes = map[string]bool{
	"device.approve":    true,
	"device.reject":     true,
	"device.revoke":     true,
	"device.store_code": true,
	"device.test_ring":  true,
	"agent.config":      true,
	"agent.release":     true,
	"note":              true, // ghi chú tự do của admin (dùng để thử cụm)
}

// AppType: loại quyết định của ứng dụng (quá nửa) có hợp lệ không.
func AppType(t string) bool { return appTypes[t] }

// Trạng thái node trong danh sách cụm.
const (
	StatusActive   = "active"
	StatusIsolated = "isolated"
)

// Decision: một quyết định trong nhật ký. Chữ ký phủ mọi trường trừ Sigs.
type Decision struct {
	Seq      uint64            `json:"seq"`
	Round    uint32            `json:"round"`
	Prev     string            `json:"prev"` // mã băm quyết định trước (hex); quyết định đầu tiên để trống
	Type     string            `json:"type"`
	Payload  json.RawMessage   `json:"payload"`
	Proposer string            `json:"proposer"` // mã node đề nghị
	Admin    string            `json:"admin,omitempty"`
	Created  time.Time         `json:"created"`
	Nonce    string            `json:"nonce"`
	Sigs     map[string]string `json:"sigs,omitempty"` // mã node → chữ ký base64
}

// Addrs: địa chỉ của một node, theo cổng.
type Addrs struct {
	Cluster []string `json:"cluster"`         // https://...:5222 cho node khác
	Agent   []string `json:"agent,omitempty"` // https://...:6222 cho agent
}

// Member: một node trong danh sách cụm.
type Member struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	PubKey string `json:"pub"` // Ed25519, base64
	Addrs  Addrs  `json:"addrs"`
	Status string `json:"status"`
}

// MemberAddPayload: thêm một node vào cụm.
type MemberAddPayload struct {
	Member Member `json:"member"`
}

// GenesisPayload: quyết định đầu tiên tạo cụm với một node.
type GenesisPayload struct {
	Cluster string `json:"cluster"` // tên cụm
	Member  Member `json:"member"`
}

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	idRe   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	hexRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// NodeID: 16 ký tự hex đầu của SHA-256 khóa công khai.
func NodeID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// EncodePub / DecodePub: khóa công khai dạng base64 trong danh sách cụm.
func EncodePub(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

func DecodePub(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("khóa công khai node không hợp lệ")
	}
	return ed25519.PublicKey(b), nil
}

// signingBytes: nội dung được ký (mọi trường trừ Sigs), kèm ngữ cảnh.
func (d *Decision) signingBytes() ([]byte, error) {
	c := *d
	c.Sigs = nil
	var buf bytes.Buffer
	buf.WriteString(sigContext)
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(&c); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Hash: mã băm của quyết định (hex), dùng làm Prev cho quyết định sau. Không tính chữ ký,
// nên thêm chữ ký không đổi mã băm.
func (d *Decision) Hash() (string, error) {
	b, err := d.signingBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Sign thêm chữ ký của node (khóa priv) vào quyết định.
func (d *Decision) Sign(priv ed25519.PrivateKey) error {
	b, err := d.signingBytes()
	if err != nil {
		return err
	}
	if d.Sigs == nil {
		d.Sigs = map[string]string{}
	}
	pub := priv.Public().(ed25519.PublicKey)
	d.Sigs[NodeID(pub)] = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b))
	return nil
}

// NewDecision tạo quyết định chưa ký với nonce mới và giờ hiện tại.
func NewDecision(seq uint64, prev, typ, proposer string, payload any, now time.Time) (*Decision, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return &Decision{Seq: seq, Round: 1, Prev: prev, Type: typ, Payload: p, Proposer: proposer,
		Created: now.UTC().Truncate(time.Second), Nonce: hex.EncodeToString(n)}, nil
}

// Genesis tạo và ký quyết định đầu tiên: cụm tên cluster với một node (khóa priv).
func Genesis(cluster, name string, addrs Addrs, priv ed25519.PrivateKey, now time.Time) (*Decision, error) {
	pub := priv.Public().(ed25519.PublicKey)
	m := Member{ID: NodeID(pub), Name: name, PubKey: EncodePub(pub), Addrs: addrs, Status: StatusActive}
	if err := m.validate(); err != nil {
		return nil, err
	}
	if !nameRe.MatchString(cluster) {
		return nil, fmt.Errorf("tên cụm %q: chữ thường, số, . _ - (tối đa 32 ký tự)", cluster)
	}
	d, err := NewDecision(1, "", TypeGenesis, m.ID, GenesisPayload{Cluster: cluster, Member: m}, now)
	if err != nil {
		return nil, err
	}
	return d, d.Sign(priv)
}

func (m *Member) validate() error {
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("tên node %q: chữ thường, số, . _ - (tối đa 32 ký tự)", m.Name)
	}
	pub, err := DecodePub(m.PubKey)
	if err != nil {
		return err
	}
	if m.ID != NodeID(pub) {
		return errors.New("mã node không khớp khóa công khai")
	}
	if m.Status != StatusActive && m.Status != StatusIsolated {
		return fmt.Errorf("trạng thái node %q không hợp lệ", m.Status)
	}
	if len(m.Addrs.Cluster) == 0 {
		return errors.New("node phải có ít nhất một địa chỉ cổng cụm (https://...:5222)")
	}
	if len(m.Addrs.Cluster) > MaxAddrs || len(m.Addrs.Agent) > MaxAddrs {
		return fmt.Errorf("tối đa %d địa chỉ mỗi loại", MaxAddrs)
	}
	for _, a := range append(slices.Clone(m.Addrs.Cluster), m.Addrs.Agent...) {
		if err := protocol.CheckServerURL(a); err != nil {
			return err
		}
	}
	return nil
}

// State: trạng thái cụm dựng từ nhật ký (danh sách cụm + quyết định cuối).
type State struct {
	Cluster   string   `json:"cluster"`    // tên cụm
	ClusterID string   `json:"cluster_id"` // mã băm quyết định đầu tiên
	Epoch     uint64   `json:"epoch"`      // seq của quyết định cuối làm đổi danh sách cụm
	Seq       uint64   `json:"seq"`        // seq quyết định cuối
	Head      string   `json:"head"`       // mã băm quyết định cuối
	Members   []Member `json:"members"`
}

// Member tìm node theo mã.
func (s *State) Member(id string) (Member, bool) {
	for _, m := range s.Members {
		if m.ID == id {
			return m, true
		}
	}
	return Member{}, false
}

// Valid: các node hợp lệ (đang hoạt động).
func (s *State) Valid() []Member {
	var out []Member
	for _, m := range s.Members {
		if m.Status == StatusActive {
			out = append(out, m)
		}
	}
	return out
}

// Trusted: khóa này có phải của một node hợp lệ không (dùng khi bắt tay TLS).
func (s *State) Trusted(pub ed25519.PublicKey) (Member, bool) {
	m, ok := s.Member(NodeID(pub))
	if !ok || m.Status != StatusActive || m.PubKey != EncodePub(pub) {
		return Member{}, false
	}
	return m, true
}

// Majority: số chữ ký quá nửa trên n node hợp lệ.
func Majority(n int) int { return n/2 + 1 }

// ErrUnsupported: loại quyết định chưa hỗ trợ ở bản này.
var ErrUnsupported = errors.New("loại quyết định chưa được hỗ trợ")

// rule: ai được ký một quyết định và cần bao nhiêu chữ ký.
type rule struct {
	eligible map[string]ed25519.PublicKey
	need     int
	must     []string // các node bắt buộc phải có chữ ký
	effect   func(next *State, hash string)
}

// Rule: ai được ký và cần bao nhiêu chữ ký (để hiện "đang chờ 2/3" trên web, hay để node đề nghị
// biết khi nào đủ).
func (s *State) Rule(d *Decision) (need int, eligible []string, err error) {
	r, err := s.check(d)
	if err != nil {
		return 0, nil, err
	}
	for id := range r.eligible {
		eligible = append(eligible, id)
	}
	sort.Strings(eligible)
	return r.need, eligible, nil
}

// Check kiểm một đề nghị trước khi ký: nối tiếp đúng, đủ trường, nội dung hợp lệ. Không đếm chữ ký.
func (s *State) Check(d *Decision) error {
	_, err := s.check(d)
	return err
}

func (s *State) validKeys() map[string]ed25519.PublicKey {
	out := map[string]ed25519.PublicKey{}
	for _, m := range s.Valid() {
		if pub, err := DecodePub(m.PubKey); err == nil {
			out[m.ID] = pub
		}
	}
	return out
}

func (s *State) check(d *Decision) (*rule, error) {
	if d == nil {
		return nil, errors.New("quyết định rỗng")
	}
	if len(d.Payload) > MaxPayload {
		return nil, errors.New("nội dung quyết định quá lớn")
	}
	if len(d.Sigs) > MaxSigs {
		return nil, errors.New("quá nhiều chữ ký")
	}
	if d.Seq != s.Seq+1 {
		return nil, fmt.Errorf("%w: quyết định số %d, node đang ở %d", ErrNotNext, d.Seq, s.Seq)
	}
	if d.Prev != s.Head {
		return nil, fmt.Errorf("%w: không nối với quyết định trước (sai mã băm)", ErrNotNext)
	}
	if !idRe.MatchString(d.Proposer) || d.Round == 0 || len(d.Nonce) != 32 || d.Created.IsZero() {
		return nil, errors.New("quyết định thiếu trường hoặc sai định dạng")
	}
	if d.Prev != "" && !hexRe.MatchString(d.Prev) {
		return nil, errors.New("mã băm quyết định trước sai định dạng")
	}
	if d.Type == TypeGenesis {
		if s.Seq != 0 {
			return nil, errors.New("cụm đã có quyết định đầu tiên")
		}
		var p GenesisPayload
		if err := decodeStrict(d.Payload, &p); err != nil {
			return nil, err
		}
		if err := p.Member.validate(); err != nil {
			return nil, err
		}
		if !nameRe.MatchString(p.Cluster) || p.Member.Status != StatusActive || d.Proposer != p.Member.ID {
			return nil, errors.New("quyết định tạo cụm không hợp lệ")
		}
		pub, _ := DecodePub(p.Member.PubKey)
		return &rule{eligible: map[string]ed25519.PublicKey{p.Member.ID: pub}, need: 1, must: []string{p.Member.ID},
			effect: func(next *State, hash string) {
				next.Cluster, next.ClusterID, next.Members, next.Epoch = p.Cluster, hash, []Member{p.Member}, d.Seq
			}}, nil
	}
	if s.Seq == 0 {
		return nil, errors.New("cụm chưa có quyết định đầu tiên")
	}
	valid := s.validKeys()
	if _, ok := valid[d.Proposer]; !ok {
		return nil, fmt.Errorf("node đề nghị %q không phải node hợp lệ của cụm", d.Proposer)
	}
	switch {
	case d.Type == TypeMemberAdd:
		var p MemberAddPayload
		if err := decodeStrict(d.Payload, &p); err != nil {
			return nil, err
		}
		if err := p.Member.validate(); err != nil {
			return nil, err
		}
		if p.Member.Status != StatusActive {
			return nil, errors.New("node mới phải ở trạng thái hoạt động")
		}
		if len(s.Members) >= MaxMembers {
			return nil, fmt.Errorf("cụm tối đa %d node", MaxMembers)
		}
		for _, m := range s.Members {
			if m.ID == p.Member.ID {
				return nil, errors.New("node đã có trong danh sách cụm (node đã gỡ phải sinh khóa mới)")
			}
			if m.Name == p.Member.Name {
				return nil, fmt.Errorf("tên node %q đã được dùng", p.Member.Name)
			}
		}
		// Thêm node: 100% node hợp lệ + chính node mới (chứng minh nó giữ khóa riêng).
		elig := map[string]ed25519.PublicKey{}
		must := []string{}
		for id, pub := range valid {
			elig[id] = pub
			must = append(must, id)
		}
		pub, _ := DecodePub(p.Member.PubKey)
		elig[p.Member.ID] = pub
		must = append(must, p.Member.ID)
		return &rule{eligible: elig, need: len(elig), must: must, effect: func(next *State, _ string) {
			next.Members = append(next.Members, p.Member)
			next.Epoch = d.Seq
		}}, nil
	case appTypes[d.Type]:
		if !json.Valid(d.Payload) || len(d.Payload) == 0 || d.Payload[0] != '{' {
			return nil, errors.New("nội dung quyết định phải là một đối tượng JSON")
		}
		return &rule{eligible: valid, need: Majority(len(valid)), must: []string{d.Proposer},
			effect: func(*State, string) {}}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnsupported, d.Type)
}

// ErrNotNext: quyết định không nối tiếp nhật ký của node (node chậm hơn hoặc nhanh hơn).
var ErrNotNext = errors.New("không nối tiếp")

// Apply kiểm quyết định d (nối tiếp, nội dung, đủ chữ ký theo quy tắc) rồi áp dụng vào trạng
// thái. Lỗi thì trạng thái giữ nguyên. Quyết định đầu tiên áp vào State rỗng.
func (s *State) Apply(d *Decision) error {
	r, err := s.check(d)
	if err != nil {
		return err
	}
	if len(d.Sigs) == 0 {
		return errors.New("quyết định không có chữ ký hợp lệ")
	}
	if err := verifySigs(d, r.eligible, r.need); err != nil {
		return err
	}
	for _, id := range r.must {
		if _, ok := d.Sigs[id]; !ok {
			return fmt.Errorf("thiếu chữ ký bắt buộc của node %q", id)
		}
	}
	hash, err := d.Hash()
	if err != nil {
		return err
	}
	next := s.clone()
	r.effect(&next, hash)
	next.Seq, next.Head = d.Seq, hash
	sort.Slice(next.Members, func(i, j int) bool { return next.Members[i].ID < next.Members[j].ID })
	*s = next
	return nil
}

// VerifySig: chữ ký của node id trên d có đúng không (dùng khi gom phiếu).
func (s *State) VerifySig(d *Decision, id string) bool {
	pub, ok := s.validKeys()[id]
	if !ok {
		if r, err := s.check(d); err == nil {
			pub, ok = r.eligible[id]
		}
	}
	sig, has := d.Sigs[id]
	if !ok || !has {
		return false
	}
	b, err := d.signingBytes()
	if err != nil {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(sig)
	return err == nil && ed25519.Verify(pub, b, raw)
}

// verifySigs: mọi chữ ký phải của node trong eligible và đúng; cần ít nhất need chữ ký.
func verifySigs(d *Decision, eligible map[string]ed25519.PublicKey, need int) error {
	b, err := d.signingBytes()
	if err != nil {
		return err
	}
	ok := 0
	for id, sig := range d.Sigs {
		pub, found := eligible[id]
		if !found {
			return fmt.Errorf("chữ ký của node %q không được tính cho quyết định này", id)
		}
		raw, err := base64.StdEncoding.DecodeString(sig)
		if err != nil || !ed25519.Verify(pub, b, raw) {
			return fmt.Errorf("chữ ký của node %q sai", id)
		}
		ok++
	}
	if ok < need {
		return fmt.Errorf("chưa đủ chữ ký: %d/%d", ok, need)
	}
	return nil
}

func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("nội dung quyết định sai: %w", err)
	}
	return nil
}

func (s *State) clone() State {
	c := *s
	c.Members = slices.Clone(s.Members)
	sort.Slice(c.Members, func(i, j int) bool { return c.Members[i].ID < c.Members[j].ID })
	return c
}

// Replay dựng trạng thái từ đầu nhật ký, kiểm từng quyết định.
func Replay(log []*Decision) (State, error) {
	var s State
	for _, d := range log {
		if err := s.Apply(d); err != nil {
			return State{}, fmt.Errorf("quyết định số %d: %w", d.Seq, err)
		}
	}
	return s, nil
}

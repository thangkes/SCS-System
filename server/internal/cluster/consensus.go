package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
)

// Mốc C2: đề nghị → các node ký (bỏ phiếu) → đủ chữ ký thì ghi vào nhật ký → phát cho mọi node.
// Luật bỏ phiếu ở common/cluster (vote.go). File này lo phần của từng node: giữ phiếu đã bỏ
// (votes.json, để khởi động lại không ký trùng), gom chữ ký, bắt kịp nhật ký, phát hiện tách nhánh.

// VotesFile: phiếu đã bỏ cho quyết định kế tiếp.
const VotesFile = "votes.json"

// Số lần thử và thời gian chờ của một đề nghị (test đổi được).
var (
	ProposeAttempts = 8
	PeerTimeout     = 5 * time.Second
	BackoffMax      = 2 * time.Second
	MaxLogBatch     = 500
)

// ProposeReq / ProposeResp: node đề nghị xin chữ ký của node khác.
type ProposeReq struct {
	Decision *cc.Decision   `json:"decision"`
	Proof    []*cc.Decision `json:"proof,omitempty"` // bằng chứng round trước không ai đủ phiếu (round > 1)
	// DryRun: chỉ hỏi "node có sẵn sàng ký không" (đã được admin xác nhận, nội dung hợp lệ), không bỏ phiếu.
	DryRun bool `json:"dry_run,omitempty"`
}

type ProposeResp struct {
	Sig   string       `json:"sig,omitempty"`   // chữ ký của node trả lời (đồng ý)
	OK    bool         `json:"ok,omitempty"`    // trả lời cho DryRun: sẵn sàng ký
	Seq   uint64       `json:"seq"`             // node trả lời đang ở quyết định số mấy
	Voted *cc.Decision `json:"voted,omitempty"` // đề nghị khác node này đã ký ở cùng (seq, round)
	Error string       `json:"error,omitempty"`
}

// Transport: cách gọi node khác (HTTP qua 5222; test dùng bản trong bộ nhớ).
type Transport interface {
	Propose(ctx context.Context, peer cc.Member, req ProposeReq) (ProposeResp, error)
	Commit(ctx context.Context, peer cc.Member, d *cc.Decision) error
	Log(ctx context.Context, peer cc.Member, from uint64) ([]*cc.Decision, error)
	// JoinSign: xin chữ ký của node mới (chưa thuộc cụm) trên quyết định thêm chính nó.
	JoinSign(ctx context.Context, m cc.Member, d *cc.Decision) (string, error)
	// ForwardJoin: chuyển một yêu cầu xin vào cụm cho node khác.
	ForwardJoin(ctx context.Context, peer cc.Member, m cc.Member) error
}

// ErrPending: đề nghị hợp lệ nhưng còn chờ admin xác nhận trên node khác.
var ErrPending = errors.New("chưa đủ xác nhận")

// ErrNotApproved: admin chưa xác nhận quyết định member.* này trên node.
var ErrNotApproved = errors.New("admin chưa xác nhận việc này trên node này")

// ErrFork: nhật ký của cụm bị tách nhánh (hai quyết định khác nhau cùng đủ chữ ký cho một seq).
// Chỉ xảy ra khi có node ký hai đề nghị trái nhau. Node dừng đề nghị và bỏ phiếu tới khi admin xử lý.
var ErrFork = errors.New("nhật ký cụm bị tách nhánh, dừng ra quyết định (xem cluster status)")

type voteState struct {
	Votes     cc.Votes                `json:"votes"`
	Decisions map[uint32]*cc.Decision `json:"decisions"` // round → đề nghị đã ký (kèm chữ ký của node này)
}

// consensus: phần trạng thái thêm của Node cho C2.
type consensus struct {
	mu        sync.Mutex // tuần tự hóa bỏ phiếu / ghi nhật ký
	votes     voteState
	hashes    []string // mã băm quyết định theo seq (hashes[0] = seq 1)
	fork      string   // khác rỗng: mô tả tách nhánh
	equivoc   map[string]string
	transport Transport
	// Validate: server kiểm nội dung quyết định của ứng dụng trước khi ký (nil = không kiểm thêm).
	validate func(*cc.Decision) error
	// OnApply: gọi sau khi một quyết định được ghi vào nhật ký (áp vào DB).
	onApply func(*cc.Decision)
}

// SetTransport, SetValidate, SetOnApply: gắn phần phụ thuộc (gọi trước khi chạy).
func (n *Node) SetTransport(t Transport)               { n.c.transport = t }
func (n *Node) SetValidate(f func(*cc.Decision) error) { n.c.validate = f }
func (n *Node) SetOnApply(f func(*cc.Decision))        { n.c.onApply = f }

// Fork: mô tả tách nhánh nếu có. Equivocators: node đã ký hai đề nghị trái nhau (mã node → bằng chứng).
func (n *Node) Fork() string {
	n.c.mu.Lock()
	defer n.c.mu.Unlock()
	return n.c.fork
}

func (n *Node) Equivocators() map[string]string {
	n.c.mu.Lock()
	defer n.c.mu.Unlock()
	out := map[string]string{}
	for k, v := range n.c.equivoc {
		out[k] = v
	}
	return out
}

func (n *Node) loadVotes() error {
	b, err := os.ReadFile(filepath.Join(n.Dir, VotesFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &n.c.votes)
}

// saveVotes ghi phiếu xuống đĩa TRƯỚC khi trả chữ ký (ghi file tạm rồi đổi tên).
func (n *Node) saveVotes() error {
	b, err := json.Marshal(&n.c.votes)
	if err != nil {
		return err
	}
	tmp := filepath.Join(n.Dir, VotesFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(n.Dir, VotesFile))
}

// precheck: node này có sẵn sàng ký d không (chưa xét phiếu đã bỏ). Gọi khi giữ c.mu.
func (n *Node) precheck(st *cc.State, d *cc.Decision) error {
	if n.c.fork != "" {
		return ErrFork
	}
	if err := st.Check(d); err != nil {
		return err
	}
	// Quyết định làm đổi danh sách cụm: chỉ ký khi admin đã xác nhận trên chính node này.
	if cc.MemberType(d.Type) && !n.approved(d.Type, cc.MemberTarget(d)) {
		return ErrNotApproved
	}
	if cc.AppType(d.Type) && n.c.validate != nil {
		return n.c.validate(d)
	}
	return nil
}

// vote: kiểm và ghi phiếu của node này cho d; trả d đã có thêm chữ ký của node. Gọi khi giữ c.mu.
func (n *Node) vote(d *cc.Decision, proof []*cc.Decision) (*cc.Decision, error) {
	st := n.State()
	if err := n.precheck(&st, d); err != nil {
		return nil, err
	}
	// Ghi nhận node ký hai đề nghị trái nhau (nếu đề nghị mới và đề nghị đã ký có chung người ký).
	if prev := n.c.votes.Decisions[d.Round]; prev != nil && n.c.votes.Votes.Seq == d.Seq {
		n.noteEquivocation(&st, prev, d)
	}
	v := n.c.votes.Votes
	if v.ByRound != nil {
		cp := map[uint32]string{}
		for k, h := range v.ByRound {
			cp[k] = h
		}
		v.ByRound = cp
	}
	if err := st.Vote(&v, d, proof); err != nil {
		return nil, err
	}
	signed := cloneDecision(d)
	if err := signed.Sign(n.Key); err != nil {
		return nil, err
	}
	if n.c.votes.Votes.Seq != v.Seq || n.c.votes.Decisions == nil {
		n.c.votes.Decisions = map[uint32]*cc.Decision{}
	}
	n.c.votes.Votes = v
	n.c.votes.Decisions[d.Round] = signed
	if err := n.saveVotes(); err != nil {
		return nil, fmt.Errorf("không lưu được phiếu: %w", err)
	}
	return signed, nil
}

func (n *Node) noteEquivocation(st *cc.State, a, b *cc.Decision) {
	for _, id := range st.Equivocators(a, b) {
		if n.c.equivoc == nil {
			n.c.equivoc = map[string]string{}
		}
		ha, _ := a.Hash()
		hb, _ := b.Hash()
		n.c.equivoc[id] = fmt.Sprintf("ký hai đề nghị trái nhau ở quyết định %d round %d: %s và %s", a.Seq, a.Round, short(ha), short(hb))
	}
}

func cloneDecision(d *cc.Decision) *cc.Decision {
	c := *d
	c.Payload = append([]byte(nil), d.Payload...)
	c.Sigs = map[string]string{}
	for k, v := range d.Sigs {
		c.Sigs[k] = v
	}
	return &c
}

// HandlePropose: node from xin chữ ký cho đề nghị.
func (n *Node) HandlePropose(from string, req ProposeReq) ProposeResp {
	n.c.mu.Lock()
	defer n.c.mu.Unlock()
	st := n.State()
	resp := ProposeResp{Seq: st.Seq}
	d := req.Decision
	if d == nil {
		resp.Error = "thiếu đề nghị"
		return resp
	}
	n.mu.RLock()
	cut := n.locallyIsolated(from)
	n.mu.RUnlock()
	if cut {
		resp.Error = "node gửi đang bị cô lập tại node này"
		return resp
	}
	// Node gửi phải ký đề nghị. Quyết định thường: node gửi chính là node đề nghị. Quyết định về
	// danh sách cụm có dạng duy nhất (người đề nghị là nhãn tính sẵn) nên chỉ cần node gửi đã ký.
	if !st.VerifySig(d, from) || (!cc.MemberType(d.Type) && d.Proposer != from) {
		resp.Error = "đề nghị phải do chính node gửi ký"
		return resp
	}
	if req.DryRun {
		if err := n.precheck(&st, d); err != nil {
			resp.Error = err.Error()
			return resp
		}
		resp.OK = true
		return resp
	}
	signed, err := n.vote(d, req.Proof)
	if err != nil {
		resp.Error = err.Error()
		if errors.Is(err, cc.ErrVoted) && n.c.votes.Votes.Seq == d.Seq {
			resp.Voted = n.c.votes.Decisions[d.Round]
			if resp.Voted == nil {
				resp.Voted = n.c.votes.Decisions[n.c.votes.Votes.MaxRound]
			}
		}
		return resp
	}
	resp.Sig = signed.Sigs[n.ID]
	return resp
}

// HandleCommit: nhận một quyết định đã đủ chữ ký. Trả cc.ErrNotNext nếu node còn thiếu quyết định trước.
func (n *Node) HandleCommit(d *cc.Decision) error {
	n.c.mu.Lock()
	defer n.c.mu.Unlock()
	return n.commitLocked(d)
}

func (n *Node) commitLocked(d *cc.Decision) error {
	if d == nil {
		return errors.New("thiếu quyết định")
	}
	st := n.State()
	if d.Seq <= st.Seq {
		h, err := d.Hash()
		if err != nil {
			return err
		}
		if old := n.hashAt(d.Seq); old != "" && old != h {
			// Quyết định khác cho một seq đã ghi. Chỉ coi là tách nhánh khi nó thật sự đủ chữ ký
			// của quá nửa node hợp lệ hiện tại (không thì chỉ là gói rác).
			ok := 0
			for _, m := range st.Valid() {
				if st.VerifySig(d, m.ID) {
					ok++
				}
			}
			if ok >= cc.Majority(len(st.Valid())) {
				n.c.fork = fmt.Sprintf("quyết định số %d có hai bản cùng đủ chữ ký: %s (đang giữ) và %s", d.Seq, short(old), short(h))
				return ErrFork
			}
			return errors.New("quyết định trái với nhật ký và không đủ chữ ký")
		}
		return nil // đã có
	}
	if n.c.fork != "" {
		return ErrFork
	}
	if err := n.Append(d); err != nil {
		return err
	}
	if n.c.votes.Votes.Seq <= d.Seq { // phiếu cho seq này không còn dùng
		n.c.votes = voteState{}
		_ = n.saveVotes()
	}
	n.afterMemberChange(d)
	if n.c.onApply != nil {
		n.c.onApply(d)
	}
	return nil
}

// hashAt: mã băm quyết định số seq trong nhật ký ("" nếu chưa có).
func (n *Node) hashAt(seq uint64) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if seq == 0 || seq > uint64(len(n.c.hashes)) {
		return ""
	}
	return n.c.hashes[seq-1]
}

// LogFrom: các quyết định từ số from (tối đa MaxLogBatch), đọc từ file.
func (n *Node) LogFrom(from uint64) ([]*cc.Decision, error) {
	all, err := readLog(filepath.Join(n.Dir, LogFile))
	if err != nil {
		return nil, err
	}
	if from < 1 {
		from = 1
	}
	if from > uint64(len(all)) {
		return nil, nil
	}
	out := all[from-1:]
	if len(out) > MaxLogBatch {
		out = out[:MaxLogBatch]
	}
	return out, nil
}

// peers: các node hợp lệ khác node này.
func (n *Node) peers() []cc.Member {
	var out []cc.Member
	st := n.State()
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, m := range st.Valid() {
		if m.ID != n.ID && !n.locallyIsolated(m.ID) {
			out = append(out, m)
		}
	}
	return out
}

// CatchUp hỏi các node khác những quyết định còn thiếu, kiểm chữ ký từng quyết định rồi ghi.
// Trả số quyết định đã ghi thêm.
func (n *Node) CatchUp(ctx context.Context) (int, error) {
	if n.c.transport == nil {
		return 0, nil
	}
	total := 0
	var lastErr error
	peers := n.peers()
	rand.Shuffle(len(peers), func(i, j int) { peers[i], peers[j] = peers[j], peers[i] })
	for _, p := range peers {
		for {
			cctx, cancel := context.WithTimeout(ctx, PeerTimeout)
			ds, err := n.c.transport.Log(cctx, p, n.State().Seq+1)
			cancel()
			if err != nil {
				lastErr = err
				break
			}
			got := 0
			for _, d := range ds {
				if err := n.HandleCommit(d); err != nil {
					return total, fmt.Errorf("quyết định số %d từ node %s: %w", d.Seq, p.Name, err)
				}
				got++
			}
			total += got
			if got < MaxLogBatch {
				break
			}
		}
	}
	if total == 0 && lastErr != nil && len(peers) > 0 {
		return 0, lastErr
	}
	return total, nil
}

// Propose đưa một quyết định mới vào cụm: gom chữ ký các node, đủ thì ghi và phát cho mọi node.
func (n *Node) Propose(ctx context.Context, typ string, payload any, admin string) (*cc.Decision, error) {
	if !n.Initialized() {
		return nil, ErrNotInitialized
	}
	if n.c.transport == nil {
		return nil, errors.New("chưa gắn kết nối tới node khác")
	}
	round := uint32(1)
	var proof []*cc.Decision
	var seq uint64
	var lastErr error
	for attempt := 0; attempt < ProposeAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(rand.Int64N(int64(BackoffMax)) + 1)):
			}
			_, _ = n.CatchUp(ctx)
		}
		st := n.State()
		if st.Seq+1 != seq { // quyết định trước đã xong (của node khác): bắt đầu lại từ round 1
			seq, round, proof = st.Seq+1, 1, nil
		}
		memberType := cc.MemberType(typ)
		var d *cc.Decision
		var err error
		if memberType {
			d, err = st.MemberDecision(typ, payload) // dạng duy nhất: node nào dựng cũng ra cùng một bản
		} else {
			d, err = cc.NewDecision(seq, st.Head, typ, n.ID, payload, time.Now())
		}
		if err != nil {
			return nil, err
		}
		d.Round = round
		if !memberType {
			d.Admin = admin
		}
		need, eligible, rerr := st.Rule(d)
		if rerr != nil {
			return nil, rerr
		}
		// Chỉ hỏi các node được quyền ký quyết định này (ví dụ cô lập node X thì không hỏi X).
		var peers []cc.Member
		for _, p := range n.peers() {
			if slices.Contains(eligible, p.ID) {
				peers = append(peers, p)
			}
		}
		// Quyết định về danh sách cụm: hỏi trước xem đã đủ node được admin xác nhận chưa. Chưa đủ
		// thì KHÔNG bỏ phiếu (phiếu đã bỏ không rút lại được và sẽ giữ chỗ ở seq này).
		if memberType {
			if waiting, ready := n.preflight(ctx, d, peers); !ready(need) {
				sort.Strings(waiting)
				return nil, fmt.Errorf("%w: còn chờ %s", ErrPending, strings.Join(waiting, ", "))
			}
		}

		n.c.mu.Lock()
		signed, err := n.vote(d, proof)
		var mine *cc.Decision
		if err != nil && errors.Is(err, cc.ErrVoted) {
			mine = n.c.votes.Decisions[round]
		}
		n.c.mu.Unlock()
		if err != nil && !errors.Is(err, cc.ErrVoted) {
			return nil, err
		}

		cands := []*cc.Decision{}
		behind := false
		if signed != nil {
			type res struct {
				p    cc.Member
				resp ProposeResp
				err  error
			}
			ch := make(chan res, len(peers))
			for _, p := range peers {
				go func(p cc.Member) {
					cctx, cancel := context.WithTimeout(ctx, PeerTimeout)
					defer cancel()
					r, err := n.c.transport.Propose(cctx, p, ProposeReq{Decision: signed, Proof: proof})
					ch <- res{p, r, err}
				}(p)
			}
			answered := 1
			for range peers {
				r := <-ch
				if r.err != nil {
					lastErr = fmt.Errorf("node %s không trả lời: %w", r.p.Name, r.err)
					continue
				}
				answered++
				if r.resp.Sig != "" {
					signed.Sigs[r.p.ID] = r.resp.Sig
					if !st.VerifySig(signed, r.p.ID) {
						delete(signed.Sigs, r.p.ID)
						lastErr = fmt.Errorf("node %s trả chữ ký sai", r.p.Name)
					}
					continue
				}
				if r.resp.Seq > st.Seq {
					behind = true
				}
				if r.resp.Voted != nil {
					cands = append(cands, r.resp.Voted)
				}
				lastErr = fmt.Errorf("node %s từ chối: %s", r.p.Name, r.resp.Error)
			}
			// Thêm node: còn cần chữ ký của chính node mới (chứng minh nó giữ khóa riêng).
			if typ == cc.TypeMemberAdd && len(signed.Sigs) == need-1 {
				if p, ok := payload.(cc.MemberAddPayload); ok {
					cctx, cancel := context.WithTimeout(ctx, PeerTimeout)
					sig, err := n.c.transport.JoinSign(cctx, p.Member, signed)
					cancel()
					if err != nil {
						lastErr = fmt.Errorf("node mới %s chưa ký: %w", p.Member.Name, err)
					} else {
						signed.Sigs[p.Member.ID] = sig
						if !st.VerifySig(signed, p.Member.ID) {
							delete(signed.Sigs, p.Member.ID)
							lastErr = fmt.Errorf("node mới %s trả chữ ký sai", p.Member.Name)
						}
					}
				}
			}
			if len(signed.Sigs) >= need {
				if err := n.HandleCommit(signed); err != nil {
					return nil, err
				}
				n.broadcast(ctx, signed)
				return signed, nil
			}

			cands = append(cands, signed)
			if lastErr == nil {
				lastErr = fmt.Errorf("chưa đủ chữ ký: %d/%d (%d node trả lời)", len(signed.Sigs), need, answered)
			}
		} else if mine != nil {
			cands = append(cands, mine) // node này đã ký đề nghị của node khác ở round này: chờ nó xong
			lastErr = errors.New("đang có đề nghị khác chờ đủ chữ ký")
		}
		if behind {
			continue
		}
		// Một đề nghị khác ở seq này (node khác đã ký) có thể đã đủ chữ ký mà node đề nghị nó chưa
		// kịp ghi (ví dụ nó tắt giữa chừng): gộp chữ ký từ các trả lời, đủ thì ghi giúp rồi thử lại.
		if done := n.completeOther(ctx, &st, cands); done {
			continue
		}
		// Round này không ai đủ phiếu được nữa thì sang round sau, mang theo bằng chứng.
		n.c.mu.Lock()
		for _, c := range cands { // ghi nhận node ký hai đề nghị trái nhau
			for _, o := range cands {
				n.noteEquivocation(&st, c, o)
			}
		}
		n.c.mu.Unlock()
		if st.ValidAbort(seq, round, cands) == nil {
			proof, round = cands, round+1
		}
	}
	return nil, fmt.Errorf("đề nghị không đủ chữ ký sau %d lần thử: %w", ProposeAttempts, lastErr)
}

// preflight hỏi (không bỏ phiếu) các node có sẵn sàng ký d không. Trả tên các node chưa sẵn sàng và
// hàm cho biết đã đủ số node sẵn sàng chưa (tính cả node này; node mới của member.add ký sau).
func (n *Node) preflight(ctx context.Context, d *cc.Decision, peers []cc.Member) ([]string, func(need int) bool) {
	probe := cloneDecision(d)
	_ = probe.Sign(n.Key)
	type res struct {
		name string
		ok   bool
		why  string
	}
	ch := make(chan res, len(peers))
	for _, p := range peers {
		go func(p cc.Member) {
			cctx, cancel := context.WithTimeout(ctx, PeerTimeout)
			defer cancel()
			r, err := n.c.transport.Propose(cctx, p, ProposeReq{Decision: probe, DryRun: true})
			switch {
			case err != nil:
				ch <- res{p.Name, false, "node " + p.Name + " (không trả lời)"}
			case r.OK:
				ch <- res{p.Name, true, ""}
			case r.Error == ErrNotApproved.Error():
				ch <- res{p.Name, false, "admin xác nhận trên node " + p.Name}
			default:
				ch <- res{p.Name, false, "node " + p.Name + " (" + r.Error + ")"}
			}
		}(p)
	}
	ready := 1
	var waiting []string
	for range peers {
		if r := <-ch; r.ok {
			ready++
		} else {
			waiting = append(waiting, r.why)
		}
	}
	extra := 0
	if d.Type == cc.TypeMemberAdd {
		extra = 1 // chữ ký của chính node mới, xin sau
	}
	return waiting, func(need int) bool { return ready+extra >= need }
}

// completeOther gộp chữ ký của các bản giống nhau trong cands; bản nào đủ chữ ký thì ghi và phát.
func (n *Node) completeOther(ctx context.Context, st *cc.State, cands []*cc.Decision) bool {
	merged := map[string]*cc.Decision{}
	for _, c := range cands {
		h, err := c.Hash()
		if err != nil {
			continue
		}
		m := merged[h]
		if m == nil {
			m = cloneDecision(c)
			m.Sigs = map[string]string{}
			merged[h] = m
		}
		for id, sig := range c.Sigs {
			probe := cloneDecision(c)
			probe.Sigs = map[string]string{id: sig}
			if st.VerifySig(probe, id) {
				m.Sigs[id] = sig
			}
		}
	}
	for _, m := range merged {
		need, _, err := st.Rule(m)
		if err != nil || len(m.Sigs) < need {
			continue
		}
		if err := n.HandleCommit(m); err == nil {
			n.broadcast(ctx, m)
			return true
		}
	}
	return false
}

// broadcast phát quyết định đã đủ chữ ký cho mọi node (node nào lỡ thì tự bắt kịp sau).
func (n *Node) broadcast(ctx context.Context, d *cc.Decision) {
	var wg sync.WaitGroup
	for _, p := range n.peers() {
		wg.Add(1)
		go func(p cc.Member) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, PeerTimeout)
			defer cancel()
			_ = n.c.transport.Commit(cctx, p, d)
		}(p)
	}
	wg.Wait()
}

// Run: định kỳ bắt kịp nhật ký từ node khác cho tới khi ctx bị hủy.
func (n *Node) Run(ctx context.Context, every time.Duration, onErr func(error)) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		var err error
		if n.Joining() {
			var joined bool
			if joined, err = n.JoinStep(ctx); joined && onErr != nil {
				onErr(errors.New("node đã được thêm vào cụm"))
			}
		} else if n.Initialized() {
			if _, err = n.CatchUp(ctx); err == nil {
				n.TryApproved(ctx, "")
			}
		}
		if err != nil && onErr != nil && ctx.Err() == nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

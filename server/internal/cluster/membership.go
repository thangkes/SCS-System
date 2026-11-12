package cluster

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
)

// Mốc C3: thêm / cô lập / bỏ cô lập / gỡ node.
//
// Mọi quyết định làm đổi danh sách cụm (member.*) chỉ được node ký khi ADMIN ĐÃ XÁC NHẬN TRÊN
// CHÍNH NODE ĐÓ. Nhờ vậy một node bị chiếm không thể tự cô lập hay gỡ node khác, hay tự kéo
// node lạ vào cụm, chỉ bằng cách gửi đề nghị: các node khác chưa có xác nhận tại chỗ sẽ không ký.
//
// Trạng thái riêng của node (không nằm trong nhật ký chung) lưu ở local.json:
//   - Joins: các yêu cầu xin vào cụm đang chờ;
//   - Approvals: việc admin đã xác nhận trên node này ("member.add:<mã node>"...);
//   - Isolated: node bị cô lập TẠI CHỖ (node này ngừng nói chuyện với nó ngay, không cần đồng thuận).
//
// Node đang xin vào cụm (chưa có nhật ký) lưu yêu cầu của nó ở join.json.

const (
	LocalFile = "local.json"
	JoinFile  = "join.json"
	// MaxJoins: số yêu cầu xin vào cụm chờ duyệt tối đa (cổng 5222 có thể mở ra Internet).
	MaxJoins = 20
	// ApprovalTTL: xác nhận của admin hết hạn sau bấy lâu nếu quyết định chưa thành.
	ApprovalTTL = 7 * 24 * time.Hour
)

// JoinRequest: một node xin vào cụm.
type JoinRequest struct {
	Member     cc.Member `json:"member"`
	ReceivedAt time.Time `json:"received_at"`
	Via        string    `json:"via,omitempty"` // mã node chuyển tiếp yêu cầu ("" = node này nhận trực tiếp)
}

type localState struct {
	Joins     map[string]JoinRequest `json:"joins,omitempty"`
	Approvals map[string]time.Time   `json:"approvals,omitempty"`
	Isolated  map[string]time.Time   `json:"isolated,omitempty"`
}

// Joining: node này đang xin vào một cụm.
type Joining struct {
	Target    string    `json:"target"`     // địa chỉ cổng cụm của node đã gửi yêu cầu tới
	TargetPub string    `json:"target_pub"` // khóa node đó (admin đã đối chiếu mã khóa)
	Member    cc.Member `json:"member"`     // thông tin node này đã khai
	Since     time.Time `json:"since"`
}

func approvalKey(typ, id string) string { return typ + ":" + id }

func (n *Node) loadLocal() error {
	for file, v := range map[string]any{LocalFile: &n.local, JoinFile: &n.joining} {
		b, err := os.ReadFile(filepath.Join(n.Dir, file))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, v); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	return nil
}

func (n *Node) writeJSON(file string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(n.Dir, file+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(n.Dir, file))
}

// saveLocal gọi khi giữ n.mu.
func (n *Node) saveLocal() error { return n.writeJSON(LocalFile, &n.local) }

// LocalView: trạng thái riêng của node để hiện trên web.
type LocalView struct {
	Joins     []JoinRequest        `json:"joins"`
	Approvals map[string]time.Time `json:"approvals"`
	Isolated  map[string]time.Time `json:"isolated"`
	Joining   *Joining             `json:"joining,omitempty"`
}

func (n *Node) Local() LocalView {
	n.mu.RLock()
	defer n.mu.RUnlock()
	v := LocalView{Approvals: map[string]time.Time{}, Isolated: map[string]time.Time{}}
	for _, j := range n.local.Joins {
		v.Joins = append(v.Joins, j)
	}
	sort.Slice(v.Joins, func(i, j int) bool { return v.Joins[i].ReceivedAt.Before(v.Joins[j].ReceivedAt) })
	for k, t := range n.local.Approvals {
		if time.Since(t) < ApprovalTTL {
			v.Approvals[k] = t
		}
	}
	for k, t := range n.local.Isolated {
		v.Isolated[k] = t
	}
	if n.joining != nil {
		j := *n.joining
		v.Joining = &j
	}
	return v
}

// approved: admin đã xác nhận việc (typ, id) trên node này và còn hạn. Gọi không giữ n.mu.
func (n *Node) approved(typ, id string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	t, ok := n.local.Approvals[approvalKey(typ, id)]
	return ok && time.Since(t) < ApprovalTTL
}

// locallyIsolated gọi khi đã giữ n.mu (đọc).
func (n *Node) locallyIsolated(id string) bool {
	_, ok := n.local.Isolated[id]
	return ok
}

// HandleJoin: một node (đã chứng minh giữ khóa pub qua TLS) xin vào cụm. via = node chuyển tiếp.
func (n *Node) HandleJoin(m cc.Member, pub ed25519.PublicKey, via string) error {
	if !n.Initialized() {
		return ErrNotInitialized
	}
	if via == "" && m.ID != cc.NodeID(pub) {
		return errors.New("yêu cầu xin vào cụm phải do chính node đó gửi (khóa không khớp)")
	}
	m.Status = cc.StatusActive
	if err := m.Validate(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.st.Member(m.ID); ok {
		return errors.New("node đã có trong cụm")
	}
	for _, r := range n.st.Removed {
		if r == m.ID {
			return errors.New("khóa node này đã bị gỡ khỏi cụm, phải sinh khóa mới để vào lại")
		}
	}
	for _, x := range n.st.Members {
		if x.Name == m.Name {
			return fmt.Errorf("tên node %q đã được dùng", m.Name)
		}
	}
	if n.local.Joins == nil {
		n.local.Joins = map[string]JoinRequest{}
	}
	if _, ok := n.local.Joins[m.ID]; !ok && len(n.local.Joins) >= MaxJoins {
		return errors.New("đang có quá nhiều yêu cầu xin vào cụm chờ duyệt")
	}
	old, had := n.local.Joins[m.ID]
	req := JoinRequest{Member: m, ReceivedAt: time.Now().UTC(), Via: via}
	if had {
		req.ReceivedAt = old.ReceivedAt
		// Node đổi thông tin khai (tên, địa chỉ) thì xác nhận cũ của admin không còn dùng được.
		if a, _ := json.Marshal(old.Member); string(a) != string(mustJSON(m)) {
			delete(n.local.Approvals, approvalKey(cc.TypeMemberAdd, m.ID))
		}
	}
	n.local.Joins[m.ID] = req
	return n.saveLocal()
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// RejectJoin: admin từ chối một yêu cầu xin vào cụm (trên node này).
func (n *Node) RejectJoin(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.local.Joins, id)
	delete(n.local.Approvals, approvalKey(cc.TypeMemberAdd, id))
	return n.saveLocal()
}

// Approve: admin xác nhận trên node này một việc làm đổi danh sách cụm (typ = member.add /
// member.isolate / member.restore / member.remove, id = mã node bị nhắm tới).
func (n *Node) Approve(typ, id string) error {
	if !cc.MemberType(typ) {
		return fmt.Errorf("loại %q không cần xác nhận tại chỗ", typ)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if typ == cc.TypeMemberAdd {
		if _, ok := n.local.Joins[id]; !ok {
			return errors.New("không có yêu cầu xin vào cụm của node này")
		}
	} else if _, ok := n.st.Member(id); !ok {
		return errors.New("node không có trong danh sách cụm")
	}
	if id == n.ID {
		return errors.New("không xác nhận quyết định về chính node này trên chính nó")
	}
	if n.local.Approvals == nil {
		n.local.Approvals = map[string]time.Time{}
	}
	n.local.Approvals[approvalKey(typ, id)] = time.Now().UTC()
	return n.saveLocal()
}

// Unapprove: rút lại xác nhận (khi quyết định chưa thành).
func (n *Node) Unapprove(typ, id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.local.Approvals, approvalKey(typ, id))
	return n.saveLocal()
}

// IsolateLocal: node này ngừng (on) / nối lại (off) liên lạc với node id ngay lập tức. Không cần
// đồng thuận vì chỉ làm giảm niềm tin của riêng node này.
func (n *Node) IsolateLocal(id string, on bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if id == n.ID {
		return errors.New("không tự cô lập chính node này")
	}
	if _, ok := n.st.Member(id); !ok {
		return errors.New("node không có trong danh sách cụm")
	}
	if on {
		if n.local.Isolated == nil {
			n.local.Isolated = map[string]time.Time{}
		}
		n.local.Isolated[id] = time.Now().UTC()
	} else {
		delete(n.local.Isolated, id)
	}
	return n.saveLocal()
}

// afterMemberChange dọn yêu cầu / xác nhận đã xong sau khi một quyết định member.* được ghi.
func (n *Node) afterMemberChange(d *cc.Decision) {
	if !cc.MemberType(d.Type) {
		return
	}
	id := cc.MemberTarget(d)
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.local.Approvals, approvalKey(d.Type, id))
	if d.Type == cc.TypeMemberAdd {
		delete(n.local.Joins, id)
	}
	if d.Type == cc.TypeMemberRemove {
		delete(n.local.Isolated, id)
		for _, t := range []string{cc.TypeMemberIsolate, cc.TypeMemberRestore} {
			delete(n.local.Approvals, approvalKey(t, id))
		}
	}
	_ = n.saveLocal()
}

// ProposeMember: đưa quyết định member.* về node id vào cụm. Node này phải đã được admin xác nhận;
// các node khác chỉ ký khi admin cũng đã xác nhận trên chúng.
func (n *Node) ProposeMember(ctx context.Context, typ, id, reason, admin string) (*cc.Decision, error) {
	if !n.approved(typ, id) {
		return nil, errors.New("admin chưa xác nhận việc này trên node này")
	}
	if typ == cc.TypeMemberAdd {
		n.mu.RLock()
		req, ok := n.local.Joins[id]
		n.mu.RUnlock()
		if !ok {
			return nil, errors.New("không có yêu cầu xin vào cụm của node này")
		}
		return n.Propose(ctx, typ, cc.MemberAddPayload{Member: req.Member}, admin)
	}
	return n.Propose(ctx, typ, cc.MemberRefPayload{ID: id, Reason: reason}, admin)
}

// TryApproved thử đưa mọi việc admin đã xác nhận trên node này vào cụm (gọi sau khi xác nhận và
// định kỳ). Việc nào chưa đủ node xác nhận thì lần sau thử lại. Trả các việc đã thành.
func (n *Node) TryApproved(ctx context.Context, admin string) []string {
	var done []string
	for key := range n.Local().Approvals {
		typ, id := splitKey(key)
		if _, err := n.ProposeMember(ctx, typ, id, "", admin); err == nil {
			done = append(done, key)
		}
	}
	return done
}

func splitKey(k string) (typ, id string) {
	for i := len(k) - 1; i >= 0; i-- {
		if k[i] == ':' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

// StartJoin: node này (chưa thuộc cụm nào) xin vào cụm qua node target có khóa targetPub.
func (n *Node) StartJoin(ctx context.Context, target string, targetPub ed25519.PublicKey, name string, addrs cc.Addrs) error {
	if n.Initialized() {
		return errors.New("node đã thuộc một cụm")
	}
	pub := n.Key.Public().(ed25519.PublicKey)
	m := cc.Member{ID: n.ID, Name: name, PubKey: cc.EncodePub(pub), Addrs: addrs, Status: cc.StatusActive}
	if err := m.Validate(); err != nil {
		return err
	}
	j := &Joining{Target: target, TargetPub: cc.EncodePub(targetPub), Member: m, Since: time.Now().UTC()}
	if err := n.sendJoin(ctx, j); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.joining = j
	return n.writeJSON(JoinFile, j)
}

func (n *Node) sendJoin(ctx context.Context, j *Joining) error {
	pub, err := cc.DecodePub(j.TargetPub)
	if err != nil {
		return err
	}
	return n.do(ctx, "POST", j.Target, "/cluster/v1/join", Pin(pub), joinReq{Member: j.Member}, nil)
}

// JoinStep: node đang xin vào cụm thử lấy nhật ký từ node đích. Lấy được nghĩa là nó đã được
// thêm: kiểm chữ ký từ quyết định đầu tiên rồi ghi. Trả true khi node đã vào cụm.
func (n *Node) JoinStep(ctx context.Context) (bool, error) {
	n.mu.RLock()
	j := n.joining
	n.mu.RUnlock()
	if n.Initialized() || j == nil {
		return n.Initialized(), nil
	}
	pub, err := cc.DecodePub(j.TargetPub)
	if err != nil {
		return false, err
	}
	var all []*cc.Decision
	for from := uint64(1); ; {
		var lr logResp
		if err := n.do(ctx, "GET", j.Target, fmt.Sprintf("/cluster/v1/log?from=%d", from), Pin(pub), nil, &lr); err != nil {
			var he *httpError
			if errors.As(err, &he) && he.Status == 403 { // chưa được thêm: gửi lại yêu cầu phòng khi node đích đã mất
				_ = n.sendJoin(ctx, j)
				return false, nil
			}
			return false, err
		}
		all = append(all, lr.Decisions...)
		if len(lr.Decisions) < MaxLogBatch {
			break
		}
		from += uint64(len(lr.Decisions))
	}
	// Kiểm toàn bộ trước khi ghi: nhật ký phải hợp lệ từ đầu và có node này trong danh sách.
	st, err := cc.Replay(all)
	if err != nil {
		return false, fmt.Errorf("nhật ký từ node đích không hợp lệ: %w", err)
	}
	if _, ok := st.Member(n.ID); !ok {
		return false, nil
	}
	if _, ok := st.Trusted(pub); !ok {
		return false, errors.New("node đích không phải node hợp lệ trong nhật ký nó gửi")
	}
	for _, d := range all {
		if err := n.HandleCommit(d); err != nil {
			return false, err
		}
	}
	n.mu.Lock()
	n.joining = nil
	n.mu.Unlock()
	_ = os.Remove(filepath.Join(n.Dir, JoinFile))
	return true, nil
}

// HandleJoinSign: node đề nghị xin chữ ký của node mới trên quyết định thêm chính nó. Chỉ ký khi
// node này đang xin vào cụm và quyết định ghi đúng thông tin nó đã khai.
func (n *Node) HandleJoinSign(d *cc.Decision) (string, error) {
	n.mu.RLock()
	j := n.joining
	n.mu.RUnlock()
	if j == nil || n.Initialized() {
		return "", errors.New("node này không đang xin vào cụm")
	}
	if d == nil || d.Type != cc.TypeMemberAdd {
		return "", errors.New("chỉ ký quyết định thêm node")
	}
	var p cc.MemberAddPayload
	if err := json.Unmarshal(d.Payload, &p); err != nil || string(mustJSON(p.Member)) != string(mustJSON(j.Member)) {
		return "", errors.New("quyết định không ghi đúng thông tin node này đã khai")
	}
	c := cloneDecision(d)
	if err := c.Sign(n.Key); err != nil {
		return "", err
	}
	return c.Sigs[n.ID], nil
}

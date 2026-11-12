package cluster

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
)

// net: mạng giả trong bộ nhớ nối các node của một cụm test.
type net struct {
	mu    sync.Mutex
	nodes map[string]*Node
	down  map[string]bool
}

type memTransport struct {
	net  *net
	self string
}

func (t memTransport) target(peer cc.Member) (*Node, error) {
	t.net.mu.Lock()
	defer t.net.mu.Unlock()
	if t.net.down[peer.ID] || t.net.down[t.self] {
		return nil, errors.New("không kết nối được")
	}
	return t.net.nodes[peer.ID], nil
}

func (t memTransport) Propose(_ context.Context, peer cc.Member, req ProposeReq) (ProposeResp, error) {
	n, err := t.target(peer)
	if err != nil {
		return ProposeResp{}, err
	}
	return n.HandlePropose(t.self, req), nil
}

func (t memTransport) Commit(_ context.Context, peer cc.Member, d *cc.Decision) error {
	n, err := t.target(peer)
	if err != nil {
		return err
	}
	return n.HandleCommit(d)
}

func (t memTransport) Log(_ context.Context, peer cc.Member, from uint64) ([]*cc.Decision, error) {
	n, err := t.target(peer)
	if err != nil {
		return nil, err
	}
	return n.LogFrom(from)
}

func (x *net) setDown(n *Node, down bool) {
	x.mu.Lock()
	x.down[n.ID] = down
	x.mu.Unlock()
}

// newCluster dựng cụm count node: node 0 tạo cụm, các node sau được thêm bằng member.add có đủ
// chữ ký của mọi node đang có và của node mới.
func newCluster(t *testing.T, count int) (*net, []*Node) {
	t.Helper()
	old := []any{ProposeAttempts, PeerTimeout, BackoffMax}
	ProposeAttempts, PeerTimeout, BackoffMax = 40, time.Second, 20*time.Millisecond
	t.Cleanup(func() {
		ProposeAttempts, PeerTimeout, BackoffMax = old[0].(int), old[1].(time.Duration), old[2].(time.Duration)
	})
	x := &net{nodes: map[string]*Node{}, down: map[string]bool{}}
	var nodes []*Node
	for i := 0; i < count; i++ {
		n := open(t)
		n.SetTransport(memTransport{x, n.ID})
		x.nodes[n.ID] = n
		if i == 0 {
			if err := n.Init("cum-thu", "node-0", addrs, time.Now()); err != nil {
				t.Fatal(err)
			}
			nodes = append(nodes, n)
			continue
		}
		st := nodes[0].State()
		pub := n.Key.Public().(ed25519.PublicKey)
		m := cc.Member{ID: n.ID, Name: fmt.Sprintf("node-%d", i), PubKey: cc.EncodePub(pub), Status: cc.StatusActive,
			Addrs: cc.Addrs{Cluster: []string{fmt.Sprintf("https://192.168.1.%d:5222", 10+i)}}}
		d, err := st.MemberDecision(cc.TypeMemberAdd, cc.MemberAddPayload{Member: m})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range append(append([]*Node{}, nodes...), n) {
			d.Sign(s.Key)
		}
		// Node mới nhận toàn bộ nhật ký từ đầu rồi quyết định thêm chính nó.
		log, _ := nodes[0].LogFrom(1)
		for _, e := range log {
			if err := n.HandleCommit(e); err != nil {
				t.Fatal(err)
			}
		}
		for _, s := range append(append([]*Node{}, nodes...), n) {
			if err := s.HandleCommit(d); err != nil {
				t.Fatalf("thêm node %d: %v", i, err)
			}
		}
		nodes = append(nodes, n)
	}
	return x, nodes
}

func sameHead(t *testing.T, nodes []*Node) cc.State {
	t.Helper()
	st := nodes[0].State()
	for _, n := range nodes[1:] {
		if s := n.State(); s.Head != st.Head || s.Seq != st.Seq {
			t.Fatalf("nhật ký lệch: node %s ở %d/%s, node đầu ở %d/%s", n.ID, s.Seq, short(s.Head), st.Seq, short(st.Head))
		}
	}
	return st
}

type note struct {
	Text string `json:"text"`
}

func TestProposeMajority(t *testing.T) {
	x, nodes := newCluster(t, 3)
	ctx := context.Background()
	base := sameHead(t, nodes).Seq

	// Đủ 3 node: đề nghị từ node nào cũng được ghi ở mọi node.
	d, err := nodes[1].Propose(ctx, "note", note{"một"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Sigs) < 2 || sameHead(t, nodes).Seq != base+1 {
		t.Fatalf("chữ ký %d", len(d.Sigs))
	}

	// Tắt 1 node: còn 2/3, vẫn ra quyết định.
	x.setDown(nodes[2], true)
	if _, err := nodes[0].Propose(ctx, "note", note{"hai"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if nodes[0].State().Seq != base+2 || nodes[2].State().Seq != base+1 {
		t.Fatal("node tắt không được nhận quyết định mới")
	}
	// Node bật lại tự bắt kịp, kiểm chữ ký từng quyết định.
	x.setDown(nodes[2], false)
	if got, err := nodes[2].CatchUp(ctx); err != nil || got != 1 {
		t.Fatalf("bắt kịp: %d %v", got, err)
	}
	sameHead(t, nodes)

	// Tắt 2 node: còn 1/3, không ra được quyết định, nhật ký không đổi.
	x.setDown(nodes[1], true)
	x.setDown(nodes[2], true)
	ProposeAttempts = 3
	if _, err := nodes[0].Propose(ctx, "note", note{"ba"}, "admin"); err == nil {
		t.Fatal("1/3 node không được ra quyết định")
	}
	if nodes[0].State().Seq != base+2 {
		t.Fatal("đề nghị thất bại không được đổi nhật ký")
	}
	// Hai node bật lại: đề nghị mới vẫn đi tiếp được (phiếu cũ không làm kẹt).
	x.setDown(nodes[1], false)
	x.setDown(nodes[2], false)
	ProposeAttempts = 40
	if _, err := nodes[1].Propose(ctx, "note", note{"bốn"}, "admin"); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		n.CatchUp(ctx)
	}
	if sameHead(t, nodes).Seq != base+3 {
		t.Fatal("sai số quyết định")
	}
}

// Nhiều node đề nghị cùng lúc: mọi đề nghị rồi cũng được ghi, nhật ký mọi node giống hệt nhau.
func TestConcurrentProposals(t *testing.T) {
	for _, count := range []int{3, 4, 5} {
		_, nodes := newCluster(t, count)
		ctx := context.Background()
		base := sameHead(t, nodes).Seq
		const each = 4
		var wg sync.WaitGroup
		var mu sync.Mutex
		okCount := 0
		for i, n := range nodes {
			wg.Add(1)
			go func(i int, n *Node) {
				defer wg.Done()
				for j := 0; j < each; j++ {
					if _, err := n.Propose(ctx, "note", note{fmt.Sprintf("n%d-%d", i, j)}, "admin"); err == nil {
						mu.Lock()
						okCount++
						mu.Unlock()
					} else {
						t.Errorf("%d node: đề nghị n%d-%d thất bại: %v", count, i, j, err)
					}
				}
			}(i, n)
		}
		wg.Wait()
		for _, n := range nodes {
			if _, err := n.CatchUp(ctx); err != nil {
				t.Fatal(err)
			}
			if f := n.Fork(); f != "" {
				t.Fatalf("tách nhánh: %s", f)
			}
			if e := n.Equivocators(); len(e) > 0 {
				t.Fatalf("node trung thực bị coi là ký hai lần: %v", e)
			}
		}
		st := sameHead(t, nodes)
		if int(st.Seq-base) != okCount || okCount != count*each {
			t.Fatalf("%d node: ghi %d quyết định, thành công %d", count, st.Seq-base, okCount)
		}
		// Mở lại từ file: nhật ký tự kiểm được từ đầu.
		for _, n := range nodes {
			n.Close()
			re, err := Open(n.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if re.State().Head != st.Head {
				t.Fatal("mở lại ra nhật ký khác")
			}
			re.Close()
		}
	}
}

// Node đã ký một đề nghị thì khởi động lại vẫn không ký đề nghị khác cùng (seq, round).
func TestVotePersisted(t *testing.T) {
	_, nodes := newCluster(t, 3)
	a, b, c := nodes[0], nodes[1], nodes[2]
	st := a.State()
	mk := func(n *Node, text string) *cc.Decision {
		d, _ := cc.NewDecision(st.Seq+1, st.Head, "note", n.ID, note{text}, time.Now())
		d.Sign(n.Key)
		return d
	}
	x, y := mk(a, "x"), mk(b, "y")
	if r := c.HandlePropose(a.ID, ProposeReq{Decision: x}); r.Sig == "" {
		t.Fatal(r.Error)
	}
	if r := c.HandlePropose(a.ID, ProposeReq{Decision: x}); r.Sig == "" {
		t.Fatal("ký lại đúng đề nghị cũ phải được")
	}
	c.Close()
	c2, err := Open(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	r := c2.HandlePropose(b.ID, ProposeReq{Decision: y})
	if r.Sig != "" || r.Voted == nil {
		t.Fatalf("sau khi khởi động lại vẫn phải từ chối đề nghị khác: %+v", r)
	}
	if h1, _ := r.Voted.Hash(); h1 != mustHash(x) {
		t.Fatal("phải trả lại đề nghị đã ký làm bằng chứng")
	}
	// Round 2 không có bằng chứng → từ chối; mạo danh node đề nghị → từ chối.
	y2 := mk(b, "y2")
	y2.Round = 2
	y2.Sigs = nil
	y2.Sign(b.Key)
	if r := c2.HandlePropose(b.ID, ProposeReq{Decision: y2}); r.Sig != "" {
		t.Fatal("round 2 không có bằng chứng phải bị từ chối")
	}
	if r := c2.HandlePropose(a.ID, ProposeReq{Decision: y}); r.Sig != "" {
		t.Fatal("đề nghị do node khác ký không được nhận dưới tên node gửi")
	}
}

func mustHash(d *cc.Decision) string { h, _ := d.Hash(); return h }

// Một node ký hai đề nghị trái nhau làm hai bản cùng đủ chữ ký: node khác phát hiện và dừng.
func TestForkDetected(t *testing.T) {
	_, nodes := newCluster(t, 3)
	a, b, c := nodes[0], nodes[1], nodes[2]
	st := a.State()
	mk := func(text string, signers ...*Node) *cc.Decision {
		d, _ := cc.NewDecision(st.Seq+1, st.Head, "note", signers[0].ID, note{text}, time.Now())
		for _, s := range signers {
			d.Sign(s.Key)
		}
		return d
	}
	x := mk("x", a, c) // c (node gian) ký cả hai
	y := mk("y", b, c)
	if err := a.HandleCommit(x); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleCommit(x); err != nil {
		t.Fatal("nhận lại đúng quyết định đã có phải êm")
	}
	if err := a.HandleCommit(y); !errors.Is(err, ErrFork) {
		t.Fatalf("phải phát hiện tách nhánh: %v", err)
	}
	if a.Fork() == "" {
		t.Fatal("phải ghi nhận tách nhánh")
	}
	ProposeAttempts = 2
	if _, err := a.Propose(context.Background(), "note", note{"z"}, "admin"); !errors.Is(err, ErrFork) {
		t.Fatalf("đã tách nhánh thì không ra quyết định mới: %v", err)
	}
	// Bản trái nhưng không đủ chữ ký chỉ là gói rác, không làm dừng cụm.
	_, nodes2 := newCluster(t, 3)
	st = nodes2[0].State()
	good, _ := cc.NewDecision(st.Seq+1, st.Head, "note", nodes2[0].ID, note{"ok"}, time.Now())
	good.Sign(nodes2[0].Key)
	good.Sign(nodes2[1].Key)
	junk, _ := cc.NewDecision(st.Seq+1, st.Head, "note", nodes2[2].ID, note{"rác"}, time.Now())
	junk.Sign(nodes2[2].Key)
	nodes2[0].HandleCommit(good)
	if err := nodes2[0].HandleCommit(junk); err == nil || errors.Is(err, ErrFork) || nodes2[0].Fork() != "" {
		t.Fatalf("gói rác không được coi là tách nhánh: %v", err)
	}
}

func TestValidateHookAndBadCommit(t *testing.T) {
	_, nodes := newCluster(t, 3)
	ctx := context.Background()
	for _, n := range nodes {
		n.SetValidate(func(d *cc.Decision) error {
			if strings.Contains(string(d.Payload), "cấm") {
				return errors.New("nội dung bị cấm")
			}
			return nil
		})
	}
	applied := 0
	nodes[2].SetOnApply(func(*cc.Decision) { applied++ })
	if _, err := nodes[0].Propose(ctx, "note", note{"cấm"}, "admin"); err == nil {
		t.Fatal("nội dung không hợp lệ phải bị từ chối")
	}
	if _, err := nodes[0].Propose(ctx, "note", note{"được"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("OnApply gọi %d lần", applied)
	}
	if _, err := nodes[0].Propose(ctx, "loai.la", note{"x"}, "admin"); err == nil {
		t.Fatal("loại quyết định lạ phải bị từ chối")
	}
	// Quyết định chỉ có 1/3 chữ ký không được ghi.
	st := nodes[0].State()
	d, _ := cc.NewDecision(st.Seq+1, st.Head, "note", nodes[0].ID, note{"thiếu"}, time.Now())
	d.Sign(nodes[0].Key)
	if err := nodes[1].HandleCommit(d); err == nil {
		t.Fatal("thiếu chữ ký không được ghi")
	}
	// Bỏ qua một quyết định (seq + 2) → báo chưa nối tiếp.
	d2, _ := cc.NewDecision(st.Seq+2, st.Head, "note", nodes[0].ID, note{"nhảy"}, time.Now())
	d2.Sign(nodes[0].Key)
	d2.Sign(nodes[1].Key)
	if err := nodes[2].HandleCommit(d2); !errors.Is(err, cc.ErrNotNext) {
		t.Fatalf("%v", err)
	}
	sameHead(t, nodes)
}

// Cụm 3 node nói chuyện qua cổng cụm thật (TLS hai chiều, ghim khóa theo danh sách cụm).
func TestHTTPCluster(t *testing.T) {
	old := []any{ProposeAttempts, PeerTimeout, BackoffMax}
	ProposeAttempts, PeerTimeout, BackoffMax = 6, 2*time.Second, 20*time.Millisecond
	defer func() {
		ProposeAttempts, PeerTimeout, BackoffMax = old[0].(int), old[1].(time.Duration), old[2].(time.Duration)
	}()
	var nodes []*Node
	var urls []string
	stops := map[string]func(){}
	for i := 0; i < 3; i++ {
		n := open(t)
		n.SetTransport(HTTPTransport{Node: n})
		cfg, err := n.ServerTLS()
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(n.Handler("test", quiet))
		srv.TLS = cfg
		srv.StartTLS()
		t.Cleanup(srv.Close)
		stops[n.ID] = srv.Close
		url := srv.URL
		a := cc.Addrs{Cluster: []string{"https://192.0.2.1:1", url}} // địa chỉ đầu chết: phải thử địa chỉ sau
		if i == 0 {
			if err := n.Init("cum-thu", "node-0", a, time.Now()); err != nil {
				t.Fatal(err)
			}
		} else {
			st := nodes[0].State()
			m := cc.Member{ID: n.ID, Name: fmt.Sprintf("node-%d", i), PubKey: cc.EncodePub(n.Key.Public().(ed25519.PublicKey)),
				Status: cc.StatusActive, Addrs: cc.Addrs{Cluster: []string{url}}}
			d, _ := st.MemberDecision(cc.TypeMemberAdd, cc.MemberAddPayload{Member: m})
			for _, s := range append(append([]*Node{}, nodes...), n) {
				d.Sign(s.Key)
			}
			for _, s := range nodes {
				if err := s.HandleCommit(d); err != nil {
					t.Fatal(err)
				}
			}
			// Node mới lấy toàn bộ nhật ký qua mạng: nó chưa có danh sách cụm nên ghim khóa node 0
			// (admin đã đối chiếu mã khóa), rồi tự kiểm chữ ký từng quyết định từ đầu.
			var lr logResp
			pub0 := nodes[0].Key.Public().(ed25519.PublicKey)
			if err := n.do(context.Background(), "GET", urls[0], "/cluster/v1/log?from=1", Pin(pub0), nil, &lr); err != nil {
				t.Fatal(err)
			}
			for _, e := range lr.Decisions {
				if err := n.HandleCommit(e); err != nil {
					t.Fatal(err)
				}
			}
		}
		nodes = append(nodes, n)
		urls = append(urls, url)
	}
	ctx := context.Background()
	base := sameHead(t, nodes).Seq
	if _, err := nodes[2].Propose(ctx, "note", note{"qua mạng"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if sameHead(t, nodes).Seq != base+1 {
		t.Fatal("chưa ghi")
	}
	// Node lạ không gọi được đường dẫn đồng thuận.
	stranger := open(t)
	pub0 := nodes[0].Key.Public().(ed25519.PublicKey)
	var lr logResp
	if err := stranger.do(ctx, "GET", urls[0], "/cluster/v1/log?from=1", Pin(pub0), nil, &lr); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("node lạ đọc được nhật ký: %v", err)
	}
	var pr ProposeResp
	if err := stranger.do(ctx, "POST", urls[0], "/cluster/v1/propose", Pin(pub0), ProposeReq{}, &pr); err == nil {
		t.Fatal("node lạ gửi được đề nghị")
	}
	// Tắt hẳn một node: 2/3 vẫn ra quyết định; node đó bật lại thì bắt kịp qua mạng.
	stops[nodes[1].ID]()
	if _, err := nodes[0].Propose(ctx, "note", note{"thiếu một node"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if nodes[2].State().Seq != base+2 || nodes[1].State().Seq != base+1 {
		t.Fatal("sai trạng thái sau khi tắt một node")
	}
	if got, err := nodes[1].CatchUp(ctx); err != nil || got != 1 {
		t.Fatalf("bắt kịp qua mạng: %d %v", got, err)
	}
	sameHead(t, nodes)
}

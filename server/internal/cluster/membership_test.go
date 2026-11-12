package cluster

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
)

func memberOf(n *Node, name, url string) cc.Member {
	return cc.Member{ID: n.ID, Name: name, PubKey: cc.EncodePub(n.Key.Public().(ed25519.PublicKey)),
		Status: cc.StatusActive, Addrs: cc.Addrs{Cluster: []string{url}}}
}

func (t memTransport) JoinSign(_ context.Context, m cc.Member, d *cc.Decision) (string, error) {
	n, err := t.target(m)
	if err != nil {
		return "", err
	}
	if n == nil {
		return "", errors.New("không tìm thấy node mới")
	}
	return n.HandleJoinSign(d)
}

func (t memTransport) ForwardJoin(_ context.Context, peer cc.Member, m cc.Member) error {
	n, err := t.target(peer)
	if err != nil {
		return err
	}
	return n.HandleJoin(m, nil, t.self)
}

// Thêm node: phải được admin xác nhận trên MỌI node; node chưa xác nhận không ký.
func TestJoinNeedsEveryNode(t *testing.T) {
	x, nodes := newCluster(t, 3)
	ctx := context.Background()
	d := open(t)
	d.SetTransport(memTransport{x, d.ID})
	x.nodes[d.ID] = d
	m := memberOf(d, "node-moi", "https://192.168.1.50:5222")
	d.joining = &Joining{Member: m}
	pubD := d.Key.Public().(ed25519.PublicKey)

	// Yêu cầu phải do chính node đó gửi; tên trùng bị từ chối.
	if err := nodes[0].HandleJoin(m, nodes[1].Key.Public().(ed25519.PublicKey), ""); err == nil {
		t.Fatal("khóa không khớp phải bị từ chối")
	}
	dup := m
	dup.Name = "node-1"
	if err := nodes[0].HandleJoin(dup, pubD, ""); err == nil {
		t.Fatal("tên trùng phải bị từ chối")
	}
	if err := nodes[0].HandleJoin(m, pubD, ""); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes[1:] { // chuyển tiếp cho các node khác
		if err := n.HandleJoin(m, nil, nodes[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	// Chưa ai xác nhận: không đề nghị được.
	if _, err := nodes[0].ProposeMember(ctx, cc.TypeMemberAdd, d.ID, "", "admin"); err == nil {
		t.Fatal("chưa xác nhận tại chỗ thì không đề nghị được")
	}
	// Node bị chiếm tự gửi đề nghị có chữ ký của nó: node khác chưa xác nhận nên không ký.
	st := nodes[0].State()
	forged, _ := st.MemberDecision(cc.TypeMemberAdd, cc.MemberAddPayload{Member: m})
	forged.Sign(nodes[0].Key)
	if r := nodes[1].HandlePropose(nodes[0].ID, ProposeReq{Decision: forged}); r.Sig != "" || r.Error != ErrNotApproved.Error() {
		t.Fatalf("node chưa xác nhận không được ký: %+v", r)
	}
	// Xác nhận lần lượt: 1/3, 2/3 vẫn chờ và nói rõ chờ node nào; 3/3 thì thành.
	nodes[0].Approve(cc.TypeMemberAdd, d.ID)
	_, err := nodes[0].ProposeMember(ctx, cc.TypeMemberAdd, d.ID, "", "admin")
	if !errors.Is(err, ErrPending) || !strings.Contains(err.Error(), "node-1") || !strings.Contains(err.Error(), "node-2") {
		t.Fatalf("phải báo còn chờ node-1, node-2: %v", err)
	}
	nodes[1].Approve(cc.TypeMemberAdd, d.ID)
	if done := nodes[1].TryApproved(ctx, "admin"); len(done) != 0 {
		t.Fatal("2/3 node xác nhận chưa đủ để thêm node")
	}
	nodes[2].Approve(cc.TypeMemberAdd, d.ID)
	if done := nodes[2].TryApproved(ctx, "admin"); len(done) != 1 {
		t.Fatalf("đủ 3/3 phải thêm được: %v", done)
	}
	for _, n := range nodes {
		st := n.State()
		if len(st.Valid()) != 4 {
			t.Fatalf("node %s thấy %d node", n.ID, len(st.Valid()))
		}
		l := n.Local()
		if len(l.Joins) != 0 || len(l.Approvals) != 0 {
			t.Fatalf("yêu cầu / xác nhận đã xong phải được dọn: %+v", l)
		}
	}
	// Node mới bắt kịp nhật ký rồi tham gia ra quyết định.
	log, _ := nodes[0].LogFrom(1)
	for _, e := range log {
		if err := d.HandleCommit(e); err != nil {
			t.Fatal(err)
		}
	}
	all := append(append([]*Node{}, nodes...), d)
	if _, err := d.Propose(ctx, "note", note{"node mới đề nghị"}, "admin"); err != nil {
		t.Fatal(err)
	}
	sameHead(t, all)
}

func TestJoinRequestLimits(t *testing.T) {
	_, nodes := newCluster(t, 1)
	a := nodes[0]
	for i := 0; i < MaxJoins; i++ {
		n := open(t)
		m := memberOf(n, fmt.Sprintf("xin-%d", i), "https://192.168.1.60:5222")
		if err := a.HandleJoin(m, n.Key.Public().(ed25519.PublicKey), ""); err != nil {
			t.Fatal(err)
		}
	}
	extra := open(t)
	if err := a.HandleJoin(memberOf(extra, "xin-thua", "https://192.168.1.60:5222"), extra.Key.Public().(ed25519.PublicKey), ""); err == nil {
		t.Fatalf("quá %d yêu cầu chờ phải bị từ chối", MaxJoins)
	}
	// Gửi lại yêu cầu cũ với thông tin đổi: xác nhận cũ của admin bị hủy.
	first := a.Local().Joins[0]
	a.Approve(cc.TypeMemberAdd, first.Member.ID)
	changed := first.Member
	changed.Addrs = cc.Addrs{Cluster: []string{"https://203.0.113.9:5222"}}
	key, _ := cc.DecodePub(changed.PubKey)
	if err := a.HandleJoin(changed, key, ""); err != nil {
		t.Fatal(err)
	}
	if len(a.Local().Approvals) != 0 {
		t.Fatal("node đổi địa chỉ sau khi được xác nhận thì xác nhận phải bị hủy")
	}
	if err := a.RejectJoin(first.Member.ID); err != nil || len(a.Local().Joins) != MaxJoins-1 {
		t.Fatal("từ chối phải xóa yêu cầu")
	}
	// Mở lại từ đĩa vẫn còn các yêu cầu.
	a.Close()
	re, err := Open(a.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	if len(re.Local().Joins) != MaxJoins-1 {
		t.Fatal("yêu cầu phải được lưu xuống đĩa")
	}
}

func TestIsolateRestoreRemoveFlow(t *testing.T) {
	_, nodes := newCluster(t, 4)
	ctx := context.Background()
	bad := nodes[3]

	// Cô lập tại chỗ: có hiệu lực ngay trên node 0, không cần node nào khác.
	if err := nodes[0].IsolateLocal(bad.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := nodes[0].Trusted(bad.Key.Public().(ed25519.PublicKey)); ok {
		t.Fatal("node bị cô lập tại chỗ không còn được tin")
	}
	st := nodes[0].State()
	p, _ := cc.NewDecision(st.Seq+1, st.Head, "note", bad.ID, note{"từ node bị cô lập"}, time.Now())
	p.Sign(bad.Key)
	if r := nodes[0].HandlePropose(bad.ID, ProposeReq{Decision: p}); r.Sig != "" {
		t.Fatal("không ký đề nghị của node bị cô lập tại chỗ")
	}
	if nodes[0].IsolateLocal(nodes[0].ID, true) == nil {
		t.Fatal("không tự cô lập chính mình")
	}

	// Cô lập toàn cụm: quá nửa của 3 node còn lại (2), mỗi node do admin xác nhận.
	nodes[0].Approve(cc.TypeMemberIsolate, bad.ID)
	if _, err := nodes[0].ProposeMember(ctx, cc.TypeMemberIsolate, bad.ID, "nghi bị tấn công", "admin"); !errors.Is(err, ErrPending) {
		t.Fatalf("một node xác nhận chưa đủ: %v", err)
	}
	// Node bị nhắm tới không tự xác nhận quyết định về chính nó.
	if err := bad.Approve(cc.TypeMemberIsolate, bad.ID); err == nil {
		t.Fatal("node không xác nhận quyết định về chính nó")
	}
	nodes[1].Approve(cc.TypeMemberIsolate, bad.ID)
	if _, err := nodes[1].ProposeMember(ctx, cc.TypeMemberIsolate, bad.ID, "nghi bị tấn công", "admin"); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes[:3] {
		n.CatchUp(ctx)
		st := n.State()
		if m, _ := st.Member(bad.ID); m.Status != cc.StatusIsolated || len(st.Valid()) != 3 {
			t.Fatalf("node %s chưa thấy cô lập", n.ID)
		}
	}
	// Cụm còn 3 node hợp lệ vẫn ra quyết định; node bị cô lập không đề nghị được.
	if _, err := nodes[2].Propose(ctx, "note", note{"sau cô lập"}, "admin"); err != nil {
		t.Fatal(err)
	}
	ProposeAttempts = 2
	bad.CatchUp(ctx)
	if _, err := bad.Propose(ctx, "note", note{"node bị cô lập"}, "admin"); err == nil {
		t.Fatal("node bị cô lập không được ra quyết định")
	}
	ProposeAttempts = 40

	// Bỏ cô lập (2/3 xác nhận), rồi gỡ hẳn.
	nodes[0].IsolateLocal(bad.ID, false)
	nodes[0].Approve(cc.TypeMemberRestore, bad.ID)
	nodes[2].Approve(cc.TypeMemberRestore, bad.ID)
	if done := nodes[2].TryApproved(ctx, "admin"); len(done) != 1 {
		t.Fatalf("bỏ cô lập: %v", done)
	}
	if st := nodes[0].State(); len(st.Valid()) != 4 {
		t.Fatal("bỏ cô lập phải hoạt động lại")
	}
	nodes[1].Approve(cc.TypeMemberRemove, bad.ID)
	nodes[2].Approve(cc.TypeMemberRemove, bad.ID)
	if done := nodes[2].TryApproved(ctx, "admin"); len(done) != 1 {
		t.Fatalf("gỡ: %v", done)
	}
	for _, n := range nodes[:3] {
		n.CatchUp(ctx)
		st := n.State()
		if _, ok := st.Member(bad.ID); ok || len(st.Removed) != 1 {
			t.Fatal("node phải bị gỡ khỏi danh sách")
		}
	}
	sameHead(t, nodes[:3])
	// Khóa đã gỡ xin vào lại bị từ chối ngay.
	if err := nodes[0].HandleJoin(memberOf(bad, "quay-lai", "https://192.168.1.70:5222"), bad.Key.Public().(ed25519.PublicKey), ""); err == nil {
		t.Fatal("khóa đã gỡ không được xin vào lại")
	}
}

// Cả luồng qua cổng cụm thật: node mới xin vào, admin duyệt trên từng node, node mới tự tải nhật ký.
func TestHTTPJoin(t *testing.T) {
	old := []any{ProposeAttempts, PeerTimeout, BackoffMax}
	ProposeAttempts, PeerTimeout, BackoffMax = 6, 2*time.Second, 20*time.Millisecond
	defer func() {
		ProposeAttempts, PeerTimeout, BackoffMax = old[0].(int), old[1].(time.Duration), old[2].(time.Duration)
	}()
	ctx := context.Background()
	serve := func(n *Node) string {
		n.SetTransport(HTTPTransport{Node: n})
		cfg, err := n.ServerTLS()
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(n.Handler("test", quiet))
		srv.TLS = cfg
		srv.StartTLS()
		t.Cleanup(srv.Close)
		return srv.URL
	}
	a, b, c := open(t), open(t), open(t)
	urlA, urlB, urlC := serve(a), serve(b), serve(c)
	pubA := a.Key.Public().(ed25519.PublicKey)
	if err := a.Init("cum-thu", "node-a", cc.Addrs{Cluster: []string{urlA}}, time.Now()); err != nil {
		t.Fatal(err)
	}

	// B xin vào qua A. Ghim sai khóa A (kẻ đứng giữa) thì không gửi được.
	if err := b.StartJoin(ctx, urlA, c.Key.Public().(ed25519.PublicKey), "node-b", cc.Addrs{Cluster: []string{urlB}}); err == nil {
		t.Fatal("ghim sai khóa node đích phải thất bại")
	}
	if err := b.StartJoin(ctx, urlA, pubA, "node-b", cc.Addrs{Cluster: []string{urlB}}); err != nil {
		t.Fatal(err)
	}
	if joined, err := b.JoinStep(ctx); joined || err != nil {
		t.Fatalf("chưa duyệt thì chưa vào được: %v %v", joined, err)
	}
	if len(a.Local().Joins) != 1 {
		t.Fatal("A phải có yêu cầu chờ")
	}
	if err := a.Approve(cc.TypeMemberAdd, b.ID); err != nil {
		t.Fatal(err)
	}
	if done := a.TryApproved(ctx, "admin"); len(done) != 1 {
		t.Fatalf("cụm 1 node: A xác nhận là đủ (cùng chữ ký của B): %v", done)
	}
	if joined, err := b.JoinStep(ctx); !joined || err != nil {
		t.Fatalf("B phải vào được cụm: %v %v", joined, err)
	}
	if b.Joining() || !b.Initialized() {
		t.Fatal("B phải hết trạng thái xin vào")
	}
	sameHead(t, []*Node{a, b})

	// C xin vào qua A: A chuyển yêu cầu cho B; phải duyệt trên cả A và B.
	if err := c.StartJoin(ctx, urlA, pubA, "node-c", cc.Addrs{Cluster: []string{urlC}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(b.Local().Joins) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(b.Local().Joins) != 1 {
		t.Fatal("yêu cầu phải được chuyển tiếp sang B")
	}
	a.Approve(cc.TypeMemberAdd, c.ID)
	if done := a.TryApproved(ctx, "admin"); len(done) != 0 {
		t.Fatal("B chưa xác nhận thì chưa thêm được C")
	}
	b.Approve(cc.TypeMemberAdd, c.ID)
	if done := b.TryApproved(ctx, "admin"); len(done) != 1 {
		t.Fatalf("A và B đã xác nhận: %v", done)
	}
	if joined, err := c.JoinStep(ctx); !joined || err != nil {
		t.Fatalf("C phải vào được cụm: %v %v", joined, err)
	}
	all := []*Node{a, b, c}
	for _, n := range all {
		n.CatchUp(ctx)
	}
	if st := sameHead(t, all); len(st.Valid()) != 3 {
		t.Fatalf("cụm phải có 3 node: %d", len(st.Valid()))
	}
	if _, err := c.Propose(ctx, "note", note{"cụm 3 node qua mạng"}, "admin"); err != nil {
		t.Fatal(err)
	}
	// Node không đang xin vào cụm không ký hộ quyết định thêm node.
	if _, err := a.HandleJoinSign(&cc.Decision{Type: cc.TypeMemberAdd}); err == nil {
		t.Fatal("node đã trong cụm không ký join-sign")
	}
}

// Hai admin xác nhận cùng một việc trên hai node rồi cả hai node cùng đề nghị một lúc: chữ ký
// cộng dồn vào một quyết định, không chia phiếu, không kẹt.
func TestConcurrentApprovers(t *testing.T) {
	for round := 0; round < 5; round++ {
		_, nodes := newCluster(t, 4)
		ctx := context.Background()
		bad := nodes[3]
		nodes[0].Approve(cc.TypeMemberIsolate, bad.ID)
		nodes[1].Approve(cc.TypeMemberIsolate, bad.ID)
		errs := make(chan error, 2)
		for _, n := range nodes[:2] {
			go func(n *Node) {
				_, err := n.ProposeMember(ctx, cc.TypeMemberIsolate, bad.ID, "", "admin")
				errs <- err
			}(n)
		}
		ok := 0
		for i := 0; i < 2; i++ {
			if err := <-errs; err == nil {
				ok++
			}
		}
		for _, n := range nodes[:3] {
			n.CatchUp(ctx)
			st := n.State()
			if m, _ := st.Member(bad.ID); m.Status != cc.StatusIsolated {
				t.Fatalf("lượt %d: node %s chưa thấy cô lập (thành công %d/2)", round, n.ID, ok)
			}
			if n.Fork() != "" {
				t.Fatal("tách nhánh")
			}
		}
		if ok == 0 {
			t.Fatalf("lượt %d: ít nhất một đề nghị phải thành", round)
		}
		sameHead(t, nodes[:3])
	}
}

// Xác nhận chưa đủ thì không bỏ phiếu: quyết định khác vẫn đi tiếp bình thường, kể cả khi một node tắt.
func TestPendingApprovalDoesNotBlock(t *testing.T) {
	x, nodes := newCluster(t, 3)
	ctx := context.Background()
	nodes[0].Approve(cc.TypeMemberIsolate, nodes[2].ID)
	if _, err := nodes[0].ProposeMember(ctx, cc.TypeMemberIsolate, nodes[2].ID, "", "admin"); !errors.Is(err, ErrPending) {
		t.Fatalf("%v", err)
	}
	x.setDown(nodes[2], true)
	if _, err := nodes[1].Propose(ctx, "note", note{"vẫn chạy"}, "admin"); err != nil {
		t.Fatalf("xác nhận dở dang không được chặn quyết định khác: %v", err)
	}
	if _, err := nodes[0].Propose(ctx, "note", note{"node đã xác nhận cũng vậy"}, "admin"); err != nil {
		t.Fatal(err)
	}
	sameHead(t, nodes[:2])
}

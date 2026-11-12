package cluster

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)

func key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

var addrs = Addrs{Cluster: []string{"https://192.168.1.10:5222"}, Agent: []string{"https://192.168.1.10:6222"}}

func TestGenesis(t *testing.T) {
	priv := key(t)
	d, err := Genesis("cum-thu", "hn-01", addrs, priv, t0)
	if err != nil {
		t.Fatal(err)
	}
	var s State
	if err := s.Apply(d); err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	if s.Seq != 1 || s.Epoch != 1 || s.Cluster != "cum-thu" || s.ClusterID != s.Head || len(s.Valid()) != 1 {
		t.Fatalf("%+v", s)
	}
	if _, ok := s.Trusted(pub); !ok {
		t.Fatal("node tạo cụm phải được tin")
	}
	if _, ok := s.Trusted(key(t).Public().(ed25519.PublicKey)); ok {
		t.Fatal("khóa lạ không được tin")
	}
	if Majority(1) != 1 || Majority(2) != 2 || Majority(3) != 2 || Majority(4) != 3 || Majority(5) != 3 {
		t.Fatal("quá nửa sai")
	}
	// Ghi ra JSON rồi đọc lại (như file nhật ký) vẫn kiểm được.
	b, _ := json.Marshal(d)
	var d2 Decision
	if err := json.Unmarshal(b, &d2); err != nil {
		t.Fatal(err)
	}
	if s2, err := Replay([]*Decision{&d2}); err != nil || s2.Head != s.Head {
		t.Fatalf("đọc lại: %v", err)
	}
}

func TestRejectTampered(t *testing.T) {
	priv := key(t)
	fresh := func() *Decision {
		d, err := Genesis("cum-thu", "hn-01", addrs, priv, t0)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	other := key(t)
	cases := map[string]func(d *Decision){
		"sửa nội dung sau khi ký": func(d *Decision) {
			d.Payload = []byte(strings.Replace(string(d.Payload), "hn-01", "hn-99", 1))
		},
		"sửa giờ":        func(d *Decision) { d.Created = d.Created.Add(time.Second) },
		"không chữ ký":   func(d *Decision) { d.Sigs = nil },
		"chữ ký khóa lạ": func(d *Decision) { d.Sigs = nil; d.Sign(other) },
		"thêm chữ ký lạ": func(d *Decision) { d.Sign(other) },
		"chữ ký hỏng": func(d *Decision) {
			for id := range d.Sigs {
				d.Sigs[id] = "AAAA"
			}
		},
		"sai seq": func(d *Decision) { d.Seq = 2 },
		"có prev": func(d *Decision) { d.Prev = strings.Repeat("a", 64) },
		"loại lạ": func(d *Decision) { d.Type = "member.add"; d.Sigs = nil; d.Sign(priv) },
		"trường lạ": func(d *Decision) {
			d.Payload = []byte(`{"cluster":"x","member":{},"them":1}`)
			d.Sigs = nil
			d.Sign(priv)
		},
		"nonce sai": func(d *Decision) { d.Nonce = "abc"; d.Sigs = nil; d.Sign(priv) },
		"node đề nghị khác": func(d *Decision) {
			d.Proposer = NodeID(other.Public().(ed25519.PublicKey))
			d.Sigs = nil
			d.Sign(priv)
		},
	}
	for name, mutate := range cases {
		d := fresh()
		mutate(d)
		var s State
		if err := s.Apply(d); err == nil {
			t.Errorf("%s: phải bị từ chối", name)
		} else if s.Seq != 0 || len(s.Members) != 0 {
			t.Errorf("%s: lỗi mà trạng thái vẫn đổi", name)
		}
	}
	// Mã node không khớp khóa trong nội dung.
	d := fresh()
	var p GenesisPayload
	json.Unmarshal(d.Payload, &p)
	p.Member.ID = strings.Repeat("0", 16)
	d.Payload, _ = json.Marshal(p)
	d.Proposer = p.Member.ID
	d.Sigs = nil
	d.Sign(priv)
	var s State
	if err := s.Apply(d); err == nil {
		t.Error("mã node giả phải bị từ chối")
	}
	// Không có quyết định đầu tiên thứ hai.
	s = State{}
	if err := s.Apply(fresh()); err != nil {
		t.Fatal(err)
	}
	g2, _ := Genesis("cum-khac", "hn-02", addrs, key(t), t0)
	g2.Seq, g2.Prev = 2, s.Head
	g2.Sigs = nil
	if err := s.Apply(g2); err == nil {
		t.Error("không được tạo cụm lần hai")
	}
}

func TestGenesisValidation(t *testing.T) {
	priv := key(t)
	bad := []struct {
		cluster, name string
		a             Addrs
	}{
		{"Cum", "hn-01", addrs},
		{"cum", "HN 01", addrs},
		{"cum", "hn-01", Addrs{}},
		{"cum", "hn-01", Addrs{Cluster: []string{"http://192.168.1.10:5222"}}},
		{"cum", "hn-01", Addrs{Cluster: []string{"https://192.168.1.10"}}},
	}
	for _, b := range bad {
		if _, err := Genesis(b.cluster, b.name, b.a, priv, t0); err == nil {
			t.Errorf("%+v: phải báo lỗi", b)
		}
	}
}

func TestHashIgnoresSigs(t *testing.T) {
	priv := key(t)
	d, _ := Genesis("cum-thu", "hn-01", addrs, priv, t0)
	h1, _ := d.Hash()
	d.Sign(key(t))
	h2, _ := d.Hash()
	if h1 != h2 {
		t.Fatal("thêm chữ ký không được đổi mã băm")
	}
}

// cluster4 dựng trạng thái cụm 4 node (thêm node cần chữ ký của mọi node đang có + node mới).
func cluster4(t *testing.T) (State, []ed25519.PrivateKey) {
	t.Helper()
	keys := []ed25519.PrivateKey{key(t), key(t), key(t), key(t)}
	g, err := Genesis("cum-thu", "n0", addrs, keys[0], t0)
	if err != nil {
		t.Fatal(err)
	}
	var s State
	if err := s.Apply(g); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 4; i++ {
		pub := keys[i].Public().(ed25519.PublicKey)
		m := Member{ID: NodeID(pub), Name: "n" + string(rune('0'+i)), PubKey: EncodePub(pub), Addrs: addrs, Status: StatusActive}
		d, _ := NewDecision(s.Seq+1, s.Head, TypeMemberAdd, NodeID(keys[0].Public().(ed25519.PublicKey)), MemberAddPayload{Member: m}, t0)
		// Thiếu chữ ký của một node đang có, hoặc của node mới → không thêm được.
		for j := 0; j < i; j++ {
			d.Sign(keys[j])
		}
		if err := s.Apply(d); err == nil {
			t.Fatal("thêm node thiếu chữ ký của node mới phải bị từ chối")
		}
		if i > 1 {
			partial, _ := NewDecision(s.Seq+1, s.Head, TypeMemberAdd, d.Proposer, MemberAddPayload{Member: m}, t0)
			partial.Sign(keys[0])
			partial.Sign(keys[i])
			if err := s.Apply(partial); err == nil {
				t.Fatal("thêm node cần 100% node hợp lệ")
			}
		}
		d.Sign(keys[i])
		if err := s.Apply(d); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.Valid()) != 4 || s.Epoch != 4 {
		t.Fatalf("%+v", s)
	}
	return s, keys
}

func TestVoteRules(t *testing.T) {
	s, keys := cluster4(t)
	id := func(i int) string { return NodeID(keys[i].Public().(ed25519.PublicKey)) }
	mk := func(round uint32, text string, signers ...int) *Decision {
		d, _ := NewDecision(s.Seq+1, s.Head, "note", id(signers[0]), map[string]string{"text": text}, t0)
		d.Round = round
		for _, i := range signers {
			d.Sign(keys[i])
		}
		return d
	}
	x, y := mk(1, "x", 0, 1), mk(1, "y", 2, 3)

	// Mỗi (seq, round) chỉ một phiếu; ký lại đúng đề nghị cũ thì được.
	var v Votes
	if err := s.Vote(&v, x, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Vote(&v, x, nil); err != nil {
		t.Fatal("ký lại đề nghị cũ phải được")
	}
	if err := s.Vote(&v, y, nil); err == nil {
		t.Fatal("không được ký đề nghị thứ hai cùng round")
	}

	// Chia 2-2 trên 4 node (cần 3): round 1 chết, được sang round 2 với bằng chứng.
	if err := s.ValidAbort(s.Seq+1, 1, []*Decision{x, y}); err != nil {
		t.Fatal(err)
	}
	z := mk(2, "z", 0)
	if err := s.Vote(&v, z, nil); err == nil {
		t.Fatal("round 2 không có bằng chứng phải bị từ chối")
	}
	if err := s.Vote(&v, z, []*Decision{x, y}); err != nil {
		t.Fatal(err)
	}
	// Đã sang round 2 thì không ký round 1 nữa.
	var v2 Votes
	s.Vote(&v2, z, []*Decision{x, y})
	if err := s.Vote(&v2, x, nil); err == nil {
		t.Fatal("đã sang round 2 không được ký round 1")
	}

	// 2 phiếu cho x, 1 cho y, còn 1 node chưa bỏ: x vẫn có thể đủ 3 → chưa được bỏ round.
	y1 := mk(1, "y", 2)
	if err := s.ValidAbort(s.Seq+1, 1, []*Decision{x, y1}); err == nil {
		t.Fatal("x còn có thể đủ phiếu, chưa được bỏ round")
	}
	// Chữ ký giả trong bằng chứng không được tính là phiếu.
	fake := mk(1, "y", 2)
	fake.Sigs[id(3)] = fake.Sigs[id(2)]
	if err := s.ValidAbort(s.Seq+1, 1, []*Decision{x, fake}); err == nil {
		t.Fatal("chữ ký giả không được tính")
	}
	// Bằng chứng lẫn round khác → từ chối.
	if err := s.ValidAbort(s.Seq+1, 1, []*Decision{x, z}); err == nil {
		t.Fatal("bằng chứng sai round phải bị từ chối")
	}

	// Node ký hai đề nghị trái nhau bị chỉ ra.
	x2 := mk(1, "x2", 0, 2)
	if e := s.Equivocators(x, x2); len(e) != 1 || e[0] != id(0) {
		t.Fatalf("%v", e)
	}
	if e := s.Equivocators(x, y); len(e) != 0 {
		t.Fatalf("%v", e)
	}

	// Quyết định ứng dụng: 2/4 chưa đủ, 3/4 đủ; chữ ký node lạ làm hỏng cả quyết định.
	st := s
	if err := st.Apply(x); err == nil {
		t.Fatal("2/4 chưa quá nửa")
	}
	ok := mk(1, "ok", 0, 1, 2)
	if err := st.Apply(ok); err != nil {
		t.Fatal(err)
	}
	if need, elig, err := s.Rule(x); err != nil || need != 3 || len(elig) != 4 {
		t.Fatalf("%d %v %v", need, elig, err)
	}
}

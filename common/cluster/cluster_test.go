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

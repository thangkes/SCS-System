package rulestore

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
)

const ruleYAML = `name: thu
version: 'V1'
rules:
  - id: SSH-FAIL-01
    title: SSH sai
    severity: medium
    source: /var/log/auth.log
    match: 'Failed password for (?P<user>\S+)'
    tests:
      match: ['sshd[1]: Failed password for root']
      no_match: ['sshd[1]: Accepted password for root']
`

func yamlVer(v, extra string) []byte {
	return []byte(strings.Replace(ruleYAML, "'V1'", "'"+v+"'", 1) + extra)
}

func newStore(t *testing.T) (*Store, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := Open(t.TempDir(), pub)
	if err != nil {
		t.Fatal(err)
	}
	return s, priv
}

func status(t *testing.T, s *Store, file string) Entry {
	t.Helper()
	e, err := s.Find(file)
	if err != nil {
		t.Fatalf("Find(%s): %v", file, err)
	}
	return e
}

func TestAddSignClassify(t *testing.T) {
	s, priv := newStore(t)
	e, err := s.Add(yamlVer("1", ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.File != "thu@1.yaml" || e.Status != StatusUnsigned || e.Rules != 1 || e.Tests != 2 {
		t.Fatalf("sau khi thêm: %+v", e)
	}
	e, err = s.Sign(e.File, priv, "kiem", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != StatusSigned || e.Signed == nil || e.Signed.By != "kiem" {
		t.Fatalf("sau khi ký: %+v", e)
	}
	// Bản ký mở được bằng khóa công khai và đúng SHA của file nguồn.
	b, err := s.Bundle("thu", "1")
	if err != nil || !strings.Contains(string(b), e.SHA256) {
		t.Fatalf("bản ký: %v %s", err, b)
	}

	// Sửa file nguồn sau khi ký mà không đổi version → cần ký lại, và không cho ký đè.
	if err := os.WriteFile(filepath.Join(s.Dir, "src", "thu@1.yaml"), yamlVer("1", "description: khac\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if got := status(t, s, "thu@1.yaml"); got.Status != StatusStale { // description nằm trong phần agent nhận
		t.Fatalf("sửa nguồn: %+v", got)
	}
	if _, err := s.Sign("thu@1.yaml", priv, "kiem", 0, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("ký đè phiên bản đã ký với nội dung khác phải bị chặn, được %v", err)
	}
	// Thêm lại qua Add cũng bị chặn.
	if _, err := s.Add(yamlVer("1", "description: khac nua\n"), nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("Add trùng phiên bản đã ký: %v", err)
	}
}

func TestInvalidAndManualFiles(t *testing.T) {
	s, _ := newStore(t)
	// Tests không đạt: Add từ chối, không ghi file.
	bad := strings.Replace(string(yamlVer("2", "")), "Accepted password", "Failed password", 1)
	if _, err := s.Add([]byte(bad), nil); err == nil {
		t.Fatal("tests sai phải bị từ chối")
	}
	// Chép tay một file lỗi và một file hợp lệ vào src/: List vẫn phân loại được.
	_ = os.WriteFile(filepath.Join(s.Dir, "src", "hong.yaml"), []byte("name: [\n"), 0o640)
	_ = os.WriteFile(filepath.Join(s.Dir, "src", "tay.yaml"), yamlVer("3", ""), 0o640)
	_ = os.WriteFile(filepath.Join(s.Dir, "src", "ten xau.yaml"), yamlVer("4", ""), 0o640)
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range list {
		got[e.File] = e.Status
	}
	want := map[string]string{"hong.yaml": StatusInvalid, "tay.yaml": StatusUnsigned, "ten xau.yaml": StatusInvalid}
	for f, st := range want {
		if got[f] != st {
			t.Errorf("%s: %q, muốn %q (%v)", f, got[f], st, got)
		}
	}
	// Hai file cùng tên + phiên bản, khác nội dung → cả hai lỗi.
	_ = os.WriteFile(filepath.Join(s.Dir, "src", "tay2.yaml"), yamlVer("3", "description: x\n"), 0o640)
	if e := status(t, s, "tay.yaml"); e.Status != StatusInvalid {
		t.Fatalf("trùng phiên bản: %+v", e)
	}
}

func TestKeyChangeAndBundleOnly(t *testing.T) {
	s, priv := newStore(t)
	e, _ := s.Add(yamlVer("5", ""), nil)
	if _, err := s.Sign(e.File, priv, "a", 0, ""); err != nil {
		t.Fatal(err)
	}
	// Đổi khóa phát hành: bản ký cũ không còn khớp → cần ký lại, và ký lại được.
	pub2, priv2, _ := ed25519.GenerateKey(rand.Reader)
	s.Pub = pub2
	if e := status(t, s, e.File); e.Status != StatusStale {
		t.Fatalf("đổi khóa: %+v", e)
	}
	if e, err := s.Sign(e.File, priv2, "a", 0, ""); err != nil || e.Status != StatusSigned {
		t.Fatalf("ký lại sau đổi khóa: %+v %v", e, err)
	}
	// Xóa nguồn, giữ bản ký: vẫn thấy trong danh sách là đã ký.
	if err := s.Remove(e.File, false); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].File != "" || list[0].Status != StatusSigned || list[0].Name != "thu" {
		t.Fatalf("chỉ còn bản ký: %+v", list)
	}
	if err := s.RemoveBundle("thu", "5"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("sau khi xóa bản ký: %+v", list)
	}
}

func TestSignWantSHAAndConflictHook(t *testing.T) {
	s, priv := newStore(t)
	hookErr := errors.New("cụm đã duyệt khác")
	if _, err := s.Add(yamlVer("6", ""), func(n, v, sha string) error { return hookErr }); !errors.Is(err, hookErr) {
		t.Fatalf("hook: %v", err)
	}
	e, _ := s.Add(yamlVer("6", ""), nil)
	if _, err := s.Sign(e.File, priv, "a", 7, strings.Repeat("0", 64)); err == nil {
		t.Fatal("SHA khác nội dung cụm duyệt phải bị chặn")
	}
	e, err := s.Sign(e.File, priv, "a", 7, e.SHA256)
	if err != nil || e.Signed.ClusterSeq != 7 {
		t.Fatalf("ký theo cụm: %+v %v", e, err)
	}
}

func TestPathSafety(t *testing.T) {
	s, _ := newStore(t)
	for _, f := range []string{"../x.yaml", "a/b.yaml", "..yaml", ""} {
		if _, err := s.Source(f); !errors.Is(err, ErrNotFound) {
			t.Errorf("Source(%q): %v", f, err)
		}
	}
	if _, err := s.Bundle("../etc", "1"); !errors.Is(err, ErrNotFound) {
		t.Error("Bundle tên lạ phải bị chặn")
	}
}

func TestApprovals(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	mk := func(seq uint64, p any) *cc.Decision {
		d, err := cc.NewDecision(seq, "", cc.TypeRulesetPublish, "x", p, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_ = d.Sign(priv)
		return d
	}
	sha := strings.Repeat("a", 64)
	log := []*cc.Decision{
		mk(2, cc.RulesetPayload{Name: "thu", Version: "1", SHA256: sha}),
		mk(3, cc.RulesetPayload{Name: "thu", Version: "1", SHA256: strings.Repeat("b", 64)}),
		mk(4, map[string]string{"name": "thu"}),
	}
	got := Approvals(log)
	if len(got) != 1 || got["thu@1"].Seq != 2 || got["thu@1"].SHA256 != sha {
		t.Fatalf("Approvals: %+v", got)
	}
}

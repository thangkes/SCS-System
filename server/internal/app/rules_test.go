package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
	"github.com/thangkes/SCS-System/server/internal/cluster"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
)

const testRuleYAML = `name: thu
version: 'V'
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

func ruleYAML(v string) string { return strings.Replace(testRuleYAML, "'V'", "'"+v+"'", 1) }

func ruleCall(t *testing.T, h http.Handler, method, path, body string, auth bool) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func rulesDeps(t *testing.T) (Deps, ed25519.PrivateKey) {
	t.Helper()
	d, _ := deps(t, nil)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := rulestore.Open(t.TempDir(), pub)
	if err != nil {
		t.Fatal(err)
	}
	d.Rules, d.ReleaseKey = s, priv
	return d, priv
}

func entryStatus(out map[string]any, file string) string {
	for _, e := range out["entries"].([]any) {
		m := e.(map[string]any)
		if m["file"] == file {
			return m["status"].(string)
		}
	}
	return ""
}

func TestRulesAPISingleServer(t *testing.T) {
	d, _ := rulesDeps(t)
	h := AdminHandler(d)

	if code, _, _ := ruleCall(t, h, "GET", "/admin/v1/rules", "", false); code != http.StatusUnauthorized {
		t.Fatalf("chưa đăng nhập xem được: %d", code)
	}
	if code, _, _ := ruleCall(t, h, "POST", "/admin/v1/rules", ruleYAML("1"), false); code != http.StatusUnauthorized {
		t.Fatalf("chưa đăng nhập thêm được: %d", code)
	}
	// Thêm: hợp lệ thì vào nhóm chưa ký; sai thì bị từ chối kèm lý do.
	if code, out, _ := ruleCall(t, h, "POST", "/admin/v1/rules", ruleYAML("1"), true); code != 200 ||
		out["entry"].(map[string]any)["status"] != rulestore.StatusUnsigned {
		t.Fatalf("thêm: %d %v", code, out)
	}
	if code, out, _ := ruleCall(t, h, "POST", "/admin/v1/rules", "name: [", true); code != 400 || out["error"] == "" {
		t.Fatalf("YAML sai: %d %v", code, out)
	}
	_, out, _ := ruleCall(t, h, "GET", "/admin/v1/rules", "", true)
	if entryStatus(out, "thu@1.yaml") != rulestore.StatusUnsigned || out["signing"] != true || out["cluster"] != nil {
		t.Fatalf("danh sách: %v", out)
	}
	// Duyệt: server đơn ký ngay.
	code, out, _ := ruleCall(t, h, "POST", "/admin/v1/rules/approve", `{"file":"thu@1.yaml"}`, true)
	if code != 200 || out["entry"].(map[string]any)["status"] != rulestore.StatusSigned || out["cluster_seq"].(float64) != 0 {
		t.Fatalf("duyệt: %d %v", code, out)
	}
	if code, _, body := ruleCall(t, h, "GET", "/admin/v1/rules/bundle?name=thu&version=1", "", true); code != 200 || !strings.Contains(body, `"sig"`) {
		t.Fatalf("tải bản ký: %d %s", code, body)
	}
	if code, _, body := ruleCall(t, h, "GET", "/admin/v1/rules/source?file=thu@1.yaml", "", true); code != 200 || !strings.Contains(body, "SSH-FAIL-01") {
		t.Fatalf("xem nguồn: %d %s", code, body)
	}
	if code, _, _ := ruleCall(t, h, "GET", "/admin/v1/rules/source?file=../../etc/passwd", "", true); code != 404 {
		t.Fatalf("đường dẫn lạ: %d", code)
	}
	if code, _, _ := ruleCall(t, h, "POST", "/admin/v1/rules/approve", `{"file":"khong-co.yaml"}`, true); code != 404 {
		t.Fatalf("duyệt file không có: %d", code)
	}
	// Không có khóa phát hành: không ký được.
	d2 := d
	d2.ReleaseKey = nil
	if code, _, _ := ruleCall(t, AdminHandler(d2), "POST", "/admin/v1/rules/approve", `{"file":"thu@1.yaml"}`, true); code != http.StatusConflict {
		t.Fatalf("không khóa mà ký được: %d", code)
	}
	// Xóa nguồn + bản ký.
	if code, _, _ := ruleCall(t, h, "POST", "/admin/v1/rules/remove", `{"file":"thu@1.yaml","bundle":true}`, true); code != 200 {
		t.Fatalf("xóa: %d", code)
	}
	if _, out, _ := ruleCall(t, h, "GET", "/admin/v1/rules", "", true); len(out["entries"].([]any)) != 0 {
		t.Fatalf("sau khi xóa: %v", out)
	}
}

// Server trong cụm: duyệt = quyết định ruleset.publish (quá nửa node) rồi mới ký. Cùng tên + phiên
// bản mà nội dung khác thì bị chặn cả khi thêm lẫn khi duyệt.
func TestRulesAPICluster(t *testing.T) {
	d, _ := rulesDeps(t)
	n, err := cluster.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if err := n.Init("cum-thu", "hn-01", cc.Addrs{Cluster: []string{"https://192.168.1.10:5222"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	n.SetTransport(cluster.HTTPTransport{Node: n}) // cụm một node: không gọi node nào khác
	d.Cluster = n
	h := AdminHandler(d)

	if code, _, _ := ruleCall(t, h, "POST", "/admin/v1/rules", ruleYAML("1"), true); code != 200 {
		t.Fatalf("thêm: %d", code)
	}
	code, out, _ := ruleCall(t, h, "POST", "/admin/v1/rules/approve", `{"file":"thu@1.yaml"}`, true)
	if code != 200 || out["cluster_seq"].(float64) != 2 {
		t.Fatalf("duyệt trong cụm: %d %v", code, out)
	}
	if st := n.State(); st.Seq != 2 {
		t.Fatalf("nhật ký cụm phải có quyết định ruleset.publish: seq %d", st.Seq)
	}
	_, out, _ = ruleCall(t, h, "GET", "/admin/v1/rules", "", true)
	e := out["entries"].([]any)[0].(map[string]any)
	if e["status"] != rulestore.StatusSigned || e["cluster"].(map[string]any)["seq"].(float64) != 2 || out["cluster"] == nil {
		t.Fatalf("danh sách trong cụm: %v", out)
	}

	// Server này xóa bộ rule (cả bản ký): cụm vẫn nhớ đã duyệt → hiện ở "cụm đã duyệt, chưa có file".
	ruleCall(t, h, "POST", "/admin/v1/rules/remove", `{"file":"thu@1.yaml","bundle":true}`, true)
	_, out, _ = ruleCall(t, h, "GET", "/admin/v1/rules", "", true)
	if only := out["cluster_only"].([]any); len(only) != 1 || only[0].(map[string]any)["name"] != "thu" {
		t.Fatalf("cụm đã duyệt mà chưa có file: %v", out["cluster_only"])
	}
	// Thêm lại cùng phiên bản nhưng nội dung khác: bị chặn.
	other := strings.Replace(ruleYAML("1"), "title: SSH sai", "title: SSH sai khac", 1)
	if code, out, _ := ruleCall(t, h, "POST", "/admin/v1/rules", other, true); code != http.StatusConflict {
		t.Fatalf("khác nội dung cụm đã duyệt: %d %v", code, out)
	}
	// Thêm lại đúng nội dung: ký luôn, không cần quyết định mới.
	ruleCall(t, h, "POST", "/admin/v1/rules", ruleYAML("1"), true)
	if code, out, _ := ruleCall(t, h, "POST", "/admin/v1/rules/approve", `{"file":"thu@1.yaml"}`, true); code != 200 || out["cluster_seq"].(float64) != 2 {
		t.Fatalf("ký lại theo quyết định cũ: %d %v", code, out)
	}
	if st := n.State(); st.Seq != 2 {
		t.Fatalf("không được tạo quyết định mới: seq %d", st.Seq)
	}
}

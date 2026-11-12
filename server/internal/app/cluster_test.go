package app

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
	"github.com/thangkes/SCS-System/server/internal/cluster"
)

func clusterCall(t *testing.T, h http.Handler, method, path, body string, auth bool) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestClusterAPI(t *testing.T) {
	// Tiến trình không chạy phần cụm: trang vẫn tải được và báo chưa bật.
	d, _ := deps(t, nil)
	if code, out := clusterCall(t, AdminHandler(d), "GET", "/admin/v1/cluster", "", true); code != 200 || out["enabled"] != false {
		t.Fatalf("%d %v", code, out)
	}

	n, err := cluster.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	d.Cluster = n
	h := AdminHandler(d)

	// Chưa đăng nhập không xem, không ghi được.
	if code, _ := clusterCall(t, h, "GET", "/admin/v1/cluster", "", false); code != http.StatusUnauthorized {
		t.Fatalf("chưa đăng nhập xem được: %d", code)
	}
	if code, _ := clusterCall(t, h, "POST", "/admin/v1/cluster/approve", `{"type":"member.add","id":"x"}`, false); code != http.StatusUnauthorized {
		t.Fatalf("chưa đăng nhập ghi được: %d", code)
	}
	// Chưa thuộc cụm: xem được mã khóa node, thao tác bị từ chối.
	code, out := clusterCall(t, h, "GET", "/admin/v1/cluster", "", true)
	if code != 200 || out["enabled"] != true || out["initialized"] != false || out["fingerprint"] == "" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := clusterCall(t, h, "POST", "/admin/v1/cluster/approve", `{"type":"member.add","id":"x"}`, true); code != http.StatusConflict {
		t.Fatalf("chưa thuộc cụm mà xác nhận được: %d", code)
	}

	if err := n.Init("cum-thu", "hn-01", cc.Addrs{Cluster: []string{"https://192.168.1.10:5222"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Một node xin vào: hiện trong danh sách chờ kèm mã khóa.
	other, err := cluster.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	pub := other.Key.Public().(ed25519.PublicKey)
	m := cc.Member{ID: other.ID, Name: "hn-02", PubKey: cc.EncodePub(pub), Status: cc.StatusActive,
		Addrs: cc.Addrs{Cluster: []string{"https://192.168.1.11:5222"}}}
	if err := n.HandleJoin(m, pub, ""); err != nil {
		t.Fatal(err)
	}
	_, out = clusterCall(t, h, "GET", "/admin/v1/cluster", "", true)
	joins := out["joins"].([]any)
	if len(joins) != 1 || joins[0].(map[string]any)["fingerprint"] != cluster.KeyFingerprint(pub) || joins[0].(map[string]any)["approved"] != false {
		t.Fatalf("%v", out["joins"])
	}
	if out["valid"].(float64) != 1 || out["majority"].(float64) != 1 || len(out["members"].([]any)) != 1 {
		t.Fatalf("%v", out)
	}
	// Body thiếu mã node, loại không hợp lệ, node không có trong cụm.
	if code, _ := clusterCall(t, h, "POST", "/admin/v1/cluster/approve", `{}`, true); code != http.StatusBadRequest {
		t.Fatalf("thiếu mã node: %d", code)
	}
	if code, _ := clusterCall(t, h, "POST", "/admin/v1/cluster/approve", `{"type":"note","id":"`+other.ID+`"}`, true); code != http.StatusConflict {
		t.Fatalf("loại lạ: %d", code)
	}
	if code, _ := clusterCall(t, h, "POST", "/admin/v1/cluster/isolate-local", `{"id":"`+other.ID+`","on":true}`, true); code != http.StatusConflict {
		t.Fatalf("cô lập node chưa trong cụm: %d", code)
	}
	if code, _ := clusterCall(t, h, "POST", "/admin/v1/cluster/approve", `{"type":"member.isolate","id":"`+n.ID+`"}`, true); code != http.StatusConflict {
		t.Fatalf("tự xác nhận về chính mình: %d", code)
	}
	// Từ chối yêu cầu: biến khỏi danh sách.
	if code, _ := clusterCall(t, h, "POST", "/admin/v1/cluster/reject-join", `{"id":"`+other.ID+`"}`, true); code != 200 {
		t.Fatalf("từ chối: %d", code)
	}
	_, out = clusterCall(t, h, "GET", "/admin/v1/cluster", "", true)
	if len(out["joins"].([]any)) != 0 {
		t.Fatal("yêu cầu đã từ chối phải biến mất")
	}
}

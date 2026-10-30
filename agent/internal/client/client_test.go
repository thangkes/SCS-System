package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/agent/internal/identity"
)

// Địa chỉ lúc cài không kết nối được → tự thử địa chỉ server gửi xuống, được thì dùng tiếp.
func TestFailoverToNextServer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	key := &identity.Key{Public: pub, Private: priv}

	dead := "https://127.0.0.1:1" // không có gì nghe
	c, err := New(dead, ca, dir, key, "test")
	if err != nil {
		t.Fatal(err)
	}
	c.SetServers([]string{srv.URL, "khong-hop-le", dead})
	if got := c.Servers(); len(got) != 2 || got[0] != dead || got[1] != srv.URL {
		t.Fatalf("danh sách = %v (địa chỉ lúc cài đứng đầu, bỏ địa chỉ sai và trùng)", got)
	}
	var out struct{ OK bool }
	if err := c.Send(context.Background(), "/v1/heartbeat", "heartbeat", "1", "fp", struct{}{}, &out); err != nil || !out.OK {
		t.Fatalf("phải chuyển sang địa chỉ còn sống: %v", err)
	}
	if c.Current() != srv.URL {
		t.Fatalf("phải dùng tiếp địa chỉ vừa được: %s", c.Current())
	}
	// Danh sách mới vẫn có địa chỉ đang dùng → giữ nguyên.
	c.SetServers([]string{"https://10.0.0.1:6222", srv.URL})
	if c.Current() != srv.URL {
		t.Fatalf("SetServers không được đổi địa chỉ đang chạy tốt: %s", c.Current())
	}
}

// Gọi server bằng tên không có trong chứng chỉ (localhost; chứng chỉ chỉ ghi 127.0.0.1, example.com)
// vẫn được, vì agent chỉ kiểm chứng chỉ do CA của server ký.
func TestPingAnyAddress(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	addr := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	c, err := New(addr, ca, dir, &identity.Key{Public: pub, Private: priv}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.Ping(context.Background()); err != nil || got != addr {
		t.Fatalf("ping %s: %v", addr, err)
	}
}

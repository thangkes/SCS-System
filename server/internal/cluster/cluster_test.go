package cluster

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

var addrs = cc.Addrs{Cluster: []string{"https://192.168.1.10:5222"}}

func open(t *testing.T) *Node {
	t.Helper()
	n, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

func TestKeyCreateAndReload(t *testing.T) {
	dir := t.TempDir()
	k1, created, err := LoadOrCreateKey(dir)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	k2, created, err := LoadOrCreateKey(dir)
	if err != nil || created || !k1.Equal(k2) {
		t.Fatal("đọc lại phải ra đúng khóa cũ")
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, KeyFile))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("quyền khóa %o", st.Mode().Perm())
		}
		os.Chmod(filepath.Join(dir, KeyFile), 0o644)
		if _, _, err := LoadOrCreateKey(dir); err == nil {
			t.Fatal("khóa quyền 644 phải bị từ chối")
		}
	}
}

func TestInitPersistAndTamper(t *testing.T) {
	dir := t.TempDir()
	n, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n.Initialized() {
		t.Fatal("chưa init")
	}
	if err := n.Init("cum-thu", "hn-01", addrs, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := n.Init("cum-thu", "hn-01", addrs, time.Now()); err == nil {
		t.Fatal("init lần hai phải báo lỗi")
	}
	head := n.State().Head
	n.Close()

	n2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !n2.Initialized() || n2.State().Head != head || n2.ID != n.ID {
		t.Fatal("mở lại phải ra đúng cụm")
	}
	n2.Close()

	// Sửa file nhật ký: mở lại phải báo hỏng.
	p := filepath.Join(dir, LogFile)
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "hn-01", "hn-66", 1)), 0o600)
	if _, err := Open(dir); err == nil {
		t.Fatal("nhật ký bị sửa phải bị từ chối")
	}
	os.WriteFile(p, b, 0o600)

	// Thay khóa node: nhật ký không có khóa mới → từ chối.
	os.Remove(filepath.Join(dir, KeyFile))
	if _, err := Open(dir); err == nil {
		t.Fatal("khóa node không có trong danh sách cụm phải bị từ chối")
	}
}

// start chạy cổng cụm của n trên một cổng ngẫu nhiên, trả địa chỉ https://.
func start(t *testing.T, n *Node) string {
	t.Helper()
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

func TestPortAuth(t *testing.T) {
	a := open(t)
	if err := a.Init("cum-thu", "hn-01", addrs, time.Now()); err != nil {
		t.Fatal(err)
	}
	url := start(t, a)
	ctx := context.Background()
	aPub := a.Key.Public().(ed25519.PublicKey)

	// Node trong cụm (chính A) xem được trạng thái, kiểm bằng khóa trong danh sách cụm.
	var st Status
	if err := a.Get(ctx, url, "/cluster/v1/status", a.MemberOnly(), &st); err != nil {
		t.Fatal(err)
	}
	if st.State.Head != a.State().Head || st.NodeID != a.ID {
		t.Fatalf("%+v", st)
	}

	// Node lạ B: gọi hello được (để đối chiếu mã khóa), status thì bị từ chối.
	b := open(t)
	var h Hello
	if err := b.Get(ctx, url, "/cluster/v1/hello", Pin(aPub), &h); err != nil {
		t.Fatal(err)
	}
	if h.NodeID != a.ID || h.Member || h.Fingerprint != KeyFingerprint(aPub) || h.ClusterID == "" || h.Version != "" {
		t.Fatalf("%+v", h)
	}
	if err := b.Get(ctx, url, "/cluster/v1/status", Pin(aPub), &st); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("node lạ không được xem trạng thái: %v", err)
	}

	// B chưa thuộc cụm nào: không tin được A theo danh sách cụm → bắt tay thất bại.
	if err := b.Get(ctx, url, "/cluster/v1/hello", b.MemberOnly(), &h); err == nil {
		t.Fatal("B không có A trong danh sách cụm, phải từ chối A")
	}
	// Ghim sai khóa → bắt tay thất bại (kẻ đứng giữa).
	other := open(t)
	if err := b.Get(ctx, url, "/cluster/v1/hello", Pin(other.Key.Public().(ed25519.PublicKey)), &h); err == nil {
		t.Fatal("ghim sai khóa phải thất bại")
	}
	// Observe ghi lại khóa bên kia.
	var seen ed25519.PublicKey
	if err := b.Get(ctx, url, "/cluster/v1/hello", Observe(&seen), &h); err != nil || !seen.Equal(aPub) {
		t.Fatalf("observe: %v", err)
	}

	// Không gửi chứng chỉ → bắt tay thất bại.
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}}}
	if resp, err := c.Get(url + "/cluster/v1/hello"); err == nil {
		resp.Body.Close()
		t.Fatal("không có chứng chỉ phải bị từ chối")
	}
	// TLS 1.2 → bị từ chối.
	cert, _ := SelfSignedCert(b.Key)
	c12 := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}}}
	if resp, err := c12.Get(url + "/cluster/v1/hello"); err == nil {
		resp.Body.Close()
		t.Fatal("TLS 1.2 phải bị từ chối")
	}
}

func TestFingerprintFormat(t *testing.T) {
	n := open(t)
	fp := KeyFingerprint(n.Key.Public().(ed25519.PublicKey))
	if len(fp) != 39 || strings.Count(fp, "-") != 7 {
		t.Fatalf("mã khóa %q", fp)
	}
}

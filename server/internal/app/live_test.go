package app

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Luồng thông báo tức thời: mở luồng nhận ngay gói đầu, có cảnh báo mới thì nhận gói kèm cảnh báo đó,
// tắt server thì luồng đóng (không treo lúc tắt).
func TestLiveStream(t *testing.T) {
	d, reg := enrollDeps(t)
	live := NewLive(Deps{Log: d.Log, Registry: reg})
	d.Live = live
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go live.Run(ctx)
	srv := httptest.NewServer(AdminHandler(d))
	defer srv.Close()

	// Chưa đăng nhập: 401.
	if r, err := http.Get(srv.URL + "/admin/v1/stream"); err != nil || r.StatusCode != 401 {
		t.Fatalf("chưa đăng nhập: %v %v", r, err)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/admin/v1/stream", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("mở luồng: %v %v", resp, err)
	}
	defer resp.Body.Close()
	events := make(chan LivePulse, 8)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var p LivePulse
				if json.Unmarshal([]byte(data), &p) == nil && !p.At.IsZero() {
					events <- p
				}
			}
		}
	}()
	wait := func(what string, ok func(LivePulse) bool) LivePulse {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case p := <-events:
				if ok(p) {
					return p
				}
			case <-deadline:
				t.Fatalf("không nhận được: %s", what)
			}
		}
	}
	wait("gói đầu", func(p LivePulse) bool { return p.OpenAlerts == 0 })

	// Máy mới đăng ký: có cảnh báo chờ duyệt → gói mới kèm cảnh báo đó.
	a := newTestAgent(t, "12345-TN1-PRI")
	postEnroll(IngestHandler(d), a.packet(t, nil), "192.168.1.50")
	p := wait("cảnh báo mới", func(p LivePulse) bool { return p.Pending == 1 })
	if len(p.Changed) == 0 || p.Changed[0].Kind != "enroll_request" {
		t.Fatalf("gói phải kèm cảnh báo vừa mở: %+v", p)
	}

	cancel() // tắt server: luồng phải đóng
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("luồng không đóng khi tắt server")
	}
}

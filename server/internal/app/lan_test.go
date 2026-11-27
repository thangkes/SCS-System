package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/common/rules"
	"github.com/thangkes/SCS-System/server/internal/lan"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
)

// Sơ đồ mẫu với IP trung tính.
const lanYAML = `schema: 4
name: lan-mau
version: '1'
lan:
  every_hb: 7
  targets:
    - {ip: 192.168.1.10, type: can, label: "Cân 1"}
    - {ip: 192.168.1.31, type: tn, label: "TN1"}
    - {ip: 192.168.1.50, type: may_in, label: "Máy in", kind: tcp, port: 9100}
`

// fakeLANReg: kho máy giả + phần lưu LAN trong RAM (dùng đúng lan.Merge như bản PostgreSQL).
type fakeLANReg struct {
	*fakeRegistry
	mu      sync.Mutex
	site    *lan.Site
	reps    map[int64]*lan.Reporter
	changes []lan.Change
}

func (f *fakeLANReg) LANReport(_ context.Context, rep *lan.Reporter, m *rules.LAN, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rep.Store = "100"
	if f.site == nil {
		f.site, f.reps = &lan.Site{Store: "100"}, map[int64]*lan.Reporter{}
	}
	ch, saveSite, saveRep := lan.Merge(f.site, rep, f.reps[rep.DeviceID], m, now)
	if saveSite {
		f.site.SavedAt = now
	}
	if saveRep {
		rep.SavedAt = now
		f.reps[rep.DeviceID] = rep
	}
	f.changes = append(f.changes, ch...)
	return len(ch), nil
}

func (f *fakeLANReg) LANData(context.Context, string) (LANData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := LANData{Sites: map[string]*lan.Site{}, Reporters: map[string][]lan.Reporter{}, Devices: map[string][]lan.Device{}}
	if f.site != nil {
		out.Sites["100"] = f.site
		for _, r := range f.reps {
			out.Reporters["100"] = append(out.Reporters["100"], *r)
		}
		out.Devices["100"] = []lan.Device{{ID: 1, Hostname: "100-TN1-PRI"}}
	}
	return out, nil
}

func (f *fakeLANReg) LANChanges(context.Context, string, int) ([]lan.Change, error) {
	return f.changes, nil
}
func (f *fakeLANReg) PruneLAN(context.Context, time.Time) (int64, error) { return 0, nil }

func lanDeps(t *testing.T, allow string) (Deps, *fakeLANReg, *rules.LAN) {
	d, reg := enrollDeps(t)
	f := &fakeLANReg{fakeRegistry: reg}
	d.Registry = f
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := rulestore.Open(t.TempDir(), pub)
	a, err := lan.ParseAllow(allow)
	if err != nil {
		t.Fatal(err)
	}
	s.SignCheck = lan.SignCheck(a)
	d.Rules, d.ReleaseKey = s, priv
	e, err := s.Add([]byte(lanYAML), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign(e.File, priv, "a", 0, ""); err != nil {
		t.Log("không ký:", err)
		return d, f, nil
	}
	set, _, _ := s.Prepare(e.File)
	return d, f, set.LAN
}

// Server chỉ ký sơ đồ LAN khi đã khai SCS_LAN_ALLOW và mọi đích nằm trong dải.
func TestLANSignNeedsAllow(t *testing.T) {
	if _, _, m := lanDeps(t, ""); m != nil {
		t.Fatal("chưa khai SCS_LAN_ALLOW mà vẫn ký được sơ đồ LAN")
	}
	if _, _, m := lanDeps(t, "192.168.1.0/28"); m != nil { // .31 và .50 nằm ngoài /28
		t.Fatal("đích ngoài dải mà vẫn ký được")
	}
}

func TestLANIngestAndAPI(t *testing.T) {
	d, f, m := lanDeps(t, "192.168.1.0/24")
	if m == nil {
		t.Fatal("đích trong dải mà không ký được")
	}
	if len(d.Rules.LANMaps()) != 1 || d.Rules.LANMaps()[m.ListV()] == nil {
		t.Fatalf("server không đọc được sơ đồ từ bộ đã ký: %v", d.Rules.LANMaps())
	}
	ctx := context.Background()
	// Báo cáo sơ đồ lạ / sai dạng: bỏ, không ghi gì.
	ingestLAN(ctx, d, 1, &protocol.LANReport{ListV: "0123456789abcdef", N: 3, Up: "0000000000000001", State: "ok"})
	ingestLAN(ctx, d, 1, &protocol.LANReport{ListV: m.ListV(), N: 3, Up: "00000000000000ff", State: "ok"})
	if f.site != nil {
		t.Fatal("báo cáo sai mà vẫn được ghi")
	}
	ingestLAN(ctx, d, 1, &protocol.LANReport{ListV: m.ListV(), N: 3, Up: "0000000000000003", State: "ok", LANIP: "192.168.1.31"})
	if f.site == nil || len(f.reps) != 1 {
		t.Fatal("báo cáo hợp lệ không được ghi")
	}
	lanCache.Lock()
	lanCache.body = nil
	lanCache.Unlock()
	h := AdminHandler(d)
	code, out, body := ruleCall(t, h, "GET", "/admin/v1/lan", "", true)
	if code != 200 || out["enabled"] != true || !strings.Contains(body, `"store":"100"`) || !strings.Contains(body, `"present":2`) {
		t.Fatalf("GET /admin/v1/lan: %d %s", code, body)
	}
	code, _, body = ruleCall(t, h, "GET", "/admin/v1/lan/100", "", true)
	if code != 200 || !strings.Contains(body, `"tn":"khop"`) || !strings.Contains(body, `"tn_host":"100-TN1-PRI"`) {
		t.Fatalf("GET /admin/v1/lan/100: %d %s", code, body)
	}
	if code, _, _ := ruleCall(t, h, "GET", "/admin/v1/lan/abc", "", true); code != 400 {
		t.Fatalf("mã siêu thị sai phải 400, được %d", code)
	}
	if code, _, _ := ruleCall(t, h, "GET", "/admin/v1/lan", "", false); code != 401 {
		t.Fatalf("chưa đăng nhập phải 401, được %d", code)
	}
}

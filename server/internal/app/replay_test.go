package app

import (
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

// R3: cùng một gói (cùng nonce) gửi lần hai bị từ chối 409 — heartbeat, lô sự kiện, đăng ký / gia hạn; nonce
// sai định dạng cũng bị từ chối; gói mới (nonce mới) vẫn nhận bình thường.
func TestReplayRejected(t *testing.T) {
	d, reg := enrollDeps(t)
	a := newTestAgent(t, "12345-TN1-PRI")
	cert, _ := approvedAgent(t, d, reg, a)
	dev := cert.Subject.CommonName

	hb := heartbeatPacket(t, a, dev, nil)
	if w, _ := postHeartbeat(d, cert, hb); w.Code != 200 {
		t.Fatalf("heartbeat lần đầu: %d %s", w.Code, w.Body)
	}
	if w, _ := postHeartbeat(d, cert, hb); w.Code != 409 {
		t.Fatalf("gửi lại đúng gói heartbeat phải 409, được %d", w.Code)
	}
	if w, _ := postHeartbeat(d, cert, heartbeatPacket(t, a, dev, nil)); w.Code != 200 {
		t.Fatalf("heartbeat mới (nonce mới) phải nhận: %d", w.Code)
	}
	bad := heartbeatPacket(t, a, dev, func(e *protocol.Envelope) { e.Nonce = "khong-phai-hex!!" })
	if w, _ := postHeartbeat(d, cert, bad); w.Code != 409 {
		t.Fatalf("nonce sai định dạng phải bị từ chối, được %d", w.Code)
	}

	logs := logPacket(t, a, dev, []protocol.LogEvent{{RuleID: "R-01", Severity: "medium", Time: time.Now(), Count: 1}})
	if w := postLogs(d, cert, logs); w.Code != 200 {
		t.Fatalf("logs lần đầu: %d %s", w.Code, w.Body)
	}
	if w := postLogs(d, cert, logs); w.Code != 409 {
		t.Fatalf("gửi lại đúng lô sự kiện phải 409, được %d", w.Code)
	}

	// Gói đăng ký / gia hạn của máy khác (chưa duyệt): lần hai bị từ chối.
	b := newTestAgent(t, "12345-TN2-SEC")
	p := b.packet(t, nil)
	h := IngestHandler(d)
	if w, _ := postEnroll(h, p, "192.168.1.51"); w.Code != 200 && w.Code != 202 {
		t.Fatalf("đăng ký lần đầu: %d %s", w.Code, w.Body)
	}
	if w, _ := postEnroll(h, p, "192.168.1.51"); w.Code != 409 {
		t.Fatalf("gửi lại đúng gói đăng ký phải 409, được %d", w.Code)
	}
}

func TestReplayGuardExpiry(t *testing.T) {
	g := &replayGuard{seen: map[string]time.Time{}}
	now := time.Now()
	if !g.fresh("1", "abcdef0123456789", now) || g.fresh("1", "abcdef0123456789", now.Add(time.Minute)) {
		t.Fatal("lần đầu nhận, lần hai trong cửa sổ từ chối")
	}
	if !g.fresh("2", "abcdef0123456789", now) {
		t.Fatal("cùng nonce, khác máy: nhận")
	}
	if !g.fresh("1", "abcdef0123456789", now.Add(replayKeep+time.Second)) {
		t.Fatal("quá cửa sổ nhớ (gói đã ngoài giờ hợp lệ, bị chặn ở bước kiểm giờ): được ghi lại")
	}
}

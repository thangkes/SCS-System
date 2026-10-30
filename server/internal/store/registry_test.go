package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/thangkes/SCS-System/common/fleet"
	"github.com/thangkes/SCS-System/server/internal/app"
)

func enrollReq(t *testing.T, host, ip string) app.EnrollRequest {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	r := app.EnrollRequest{
		PublicKey: pub, Fingerprint: strings.Repeat("a", 64), Components: []byte(`{"machine_id":"x"}`),
		Hostname: host, OSVersion: "Ubuntu 22.04", AgentVersion: "0.1.0", IP: netip.MustParseAddr(ip),
	}
	if n, err := fleet.ParseHostname(host); err == nil {
		r.HostnameOK, r.StoreCode, r.TN, r.Role = true, n.Store, n.TN, string(n.Role)
	} else {
		r.HostnameError = err.Error()
	}
	return r
}

// Kho máy trên PostgreSQL thật: cần SCS_TEST_DATABASE_URL (xem migrate_test.go).
func TestRegistryOnPostgres(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	g := NewRegistry(pool)
	count := func(sql string, args ...any) int {
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Đăng ký mới → chờ duyệt, có cảnh báo; hỏi lại cùng khóa không tạo dòng mới.
	tn1 := enrollReq(t, "12345-TN1-PRI", "203.0.113.10")
	res, err := g.Enroll(ctx, tn1)
	if err != nil || !res.New || res.Status != app.DevicePending {
		t.Fatalf("đăng ký mới: %+v %v", res, err)
	}
	if res2, err := g.Enroll(ctx, tn1); err != nil || res2.New || res2.DeviceID != res.DeviceID {
		t.Fatalf("hỏi lại: %+v %v", res2, err)
	}
	if n := count(`SELECT count(*) FROM devices`); n != 1 {
		t.Fatalf("có %d máy, muốn 1", n)
	}
	if n := count(`SELECT count(*) FROM alerts WHERE kind = 'enroll_request' AND acked_at IS NULL`); n != 1 {
		t.Fatalf("phải có 1 cảnh báo máy chờ duyệt, có %d", n)
	}

	// Hostname sai: vào hàng chờ, có cảnh báo hostname_invalid; duyệt phải nhập mã siêu thị.
	stray, _ := g.Enroll(ctx, enrollReq(t, "ubuntu-desktop", "203.0.113.10"))
	if n := count(`SELECT count(*) FROM alerts WHERE kind = 'hostname_invalid' AND device_id = $1`, stray.DeviceID); n != 1 {
		t.Fatalf("thiếu cảnh báo hostname sai")
	}
	if _, err := g.Approve(ctx, stray.DeviceID, "Quản trị", ""); !errors.Is(err, app.ErrNeedStore) {
		t.Fatalf("duyệt máy sai hostname không nhập mã siêu thị: %v", err)
	}

	// Duyệt TN1: siêu thị nhận IP WAN, cảnh báo chờ duyệt được đóng, có audit.
	dev, err := g.Approve(ctx, res.DeviceID, "Quản trị", "")
	if err != nil || dev.Status != app.DeviceApproved || dev.StoreWANIP != "203.0.113.10" || dev.DecidedBy != "Quản trị" {
		t.Fatalf("duyệt: %+v %v", dev, err)
	}
	if n := count(`SELECT count(*) FROM audit WHERE action = 'device.approve'`); n != 1 {
		t.Fatal("thiếu audit duyệt máy")
	}
	if _, err := g.Approve(ctx, res.DeviceID, "Quản trị", ""); !errors.Is(err, app.ErrWrongState) {
		t.Fatalf("duyệt lần hai: %v", err)
	}
	if err := g.SetCertSerial(ctx, res.DeviceID, "0abc"); err != nil {
		t.Fatal(err)
	}

	// Máy khác xưng cùng vị trí TN1 từ IP lạ: cảnh báo IP; duyệt bị chặn vì trùng.
	clone, _ := g.Enroll(ctx, enrollReq(t, "12345-TN1-PRI", "198.51.100.5"))
	if n := count(`SELECT count(*) FROM alerts WHERE kind = 'wan_ip_mismatch' AND device_id = $1`, clone.DeviceID); n != 1 {
		t.Fatal("thiếu cảnh báo IP khác IP WAN của siêu thị")
	}
	if _, err := g.Approve(ctx, clone.DeviceID, "Quản trị", ""); !errors.Is(err, app.ErrConflict) {
		t.Fatalf("trùng vị trí phải ErrConflict: %v", err)
	}
	if err := g.Reject(ctx, clone.DeviceID, "Quản trị"); err != nil {
		t.Fatal(err)
	}
	if r, _ := g.Enroll(ctx, enrollReq(t, "x", "1.1.1.1")); r.Status != app.DevicePending {
		t.Fatal("máy mới khác vẫn đăng ký được")
	}

	// Danh sách theo trạng thái; thu hồi.
	if list, err := g.ListDevices(ctx, app.DeviceRejected); err != nil || len(list) != 1 || list[0].ID != clone.DeviceID {
		t.Fatalf("danh sách bị từ chối: %+v %v", list, err)
	}
	if err := g.Revoke(ctx, res.DeviceID, "Quản trị"); err != nil {
		t.Fatal(err)
	}
	if err := g.SetCertSerial(ctx, res.DeviceID, "0def"); !errors.Is(err, app.ErrWrongState) {
		t.Fatalf("máy đã thu hồi không được cấp chứng chỉ: %v", err)
	}
	if r, _ := g.Enroll(ctx, tn1); r.Status != app.DeviceRevoked {
		t.Fatalf("máy đã thu hồi hỏi lại phải thấy revoked, được %q", r.Status)
	}
	// Cảnh báo: liệt kê, xác nhận, xác nhận lần hai bị từ chối.
	open, err := g.ListAlerts(ctx, false)
	if err != nil || len(open) == 0 {
		t.Fatalf("cảnh báo đang mở: %d %v", len(open), err)
	}
	if err := g.AckAlert(ctx, open[0].ID, "Quản trị"); err != nil {
		t.Fatal(err)
	}
	if err := g.AckAlert(ctx, open[0].ID, "Quản trị"); !errors.Is(err, app.ErrWrongState) {
		t.Fatalf("xác nhận lần hai: %v", err)
	}
	if all, _ := g.ListAlerts(ctx, true); len(all) != len(open) {
		t.Fatalf("state=all phải gồm cả cảnh báo đã xác nhận: %d/%d", len(all), len(open))
	}
	if _, err := g.Approve(ctx, 999999, "Quản trị", ""); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("máy không có: %v", err)
	}
}

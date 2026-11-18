package app

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

// Gia hạn chứng chỉ: máy đã duyệt gửi lại gói enroll bằng chính khóa → server cấp chứng chỉ mới (serial
// mới, chỉ serial mới còn được nhận), lưu hạn mới; thống kê agent đếm chứng chỉ sắp hết / đã hết hạn.
func TestCertRenewalByReEnroll(t *testing.T) {
	d, reg := enrollDeps(t)
	a := newTestAgent(t, "12345-TN1-PRI")
	cert1, _ := approvedAgent(t, d, reg, a)
	serial1 := reg.serials[1]
	_, resp := postEnroll(IngestHandler(d), a.packet(t, nil), "192.168.1.50")
	if resp.Status != protocol.EnrollApproved || resp.DeviceID != "1" {
		t.Fatalf("gia hạn: %+v", resp)
	}
	blk, _ := pem.Decode([]byte(resp.CertificatePEM))
	cert2, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if reg.serials[1] == serial1 || cert2.SerialNumber.Cmp(cert1.SerialNumber) == 0 {
		t.Fatal("gia hạn phải ra serial mới")
	}
	if w, _ := postHeartbeat(d, cert1, heartbeatPacket(t, a, "1", nil)); w.Code != 403 {
		t.Fatalf("chứng chỉ cũ phải hết được nhận: %d", w.Code)
	}
	if w, _ := postHeartbeat(d, cert2, heartbeatPacket(t, a, "1", nil)); w.Code != 200 {
		t.Fatalf("chứng chỉ mới: %d", w.Code)
	}
	list, _ := reg.ListDevices(context.Background(), DeviceApproved)
	if len(list) != 1 || list[0].CertExpires.Sub(cert2.NotAfter).Abs() > time.Second {
		t.Fatalf("phải lưu hạn chứng chỉ mới: %+v", list)
	}

	rows, _ := reg.FleetRows(context.Background())
	now := time.Now()
	st := buildFleet(rows, Rollout{}, now.Add(70*24*time.Hour), time.Hour)
	if st.Certs.Soon != 1 || st.Certs.Expired != 0 {
		t.Fatalf("70 ngày sau: phải còn dưới 30 ngày: %+v", st.Certs)
	}
	st = buildFleet(rows, Rollout{}, now.Add(91*24*time.Hour), time.Hour)
	if st.Certs.Expired != 1 {
		t.Fatalf("91 ngày sau: phải hết hạn: %+v", st.Certs)
	}
}

package app

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"time"
)

// Duyệt tự động trong kho giả: cùng điều kiện, cùng thứ tự với internal/store/autoapprove.go.
// storeInfo: mã siêu thị → IP WAN trong dữ liệu đã nạp (có khóa = đã có thông tin).
type fakeAuto struct {
	on        bool
	storeInfo map[string][]string
}

func (f *fakeRegistry) AutoApproveSetting(context.Context) (AutoApproveSetting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return AutoApproveSetting{On: f.auto.on}, nil
}

func (f *fakeRegistry) SetAutoApprove(_ context.Context, on bool, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auto.on = on
	if !on {
		for _, d := range f.devices {
			d.AutoNote = ""
		}
	}
	return nil
}

func (f *fakeRegistry) AutoApprove(ctx context.Context, id int64, ip netip.Addr) (AutoApproveResult, error) {
	f.mu.Lock()
	d := f.find(id)
	if d == nil {
		f.mu.Unlock()
		return AutoApproveResult{}, ErrNotFound
	}
	if !f.auto.on {
		f.mu.Unlock()
		return AutoApproveResult{Off: true}, nil
	}
	res := AutoApproveResult{Store: d.StoreCode}
	ips, has := f.auto.storeInfo[d.StoreCode]
	switch {
	case d.Status != DevicePending:
		f.mu.Unlock()
		return res, nil
	case !d.HostnameOK || d.StoreCode == "":
		res.Reason = "Hostname sai quy ước: không đọc được mã siêu thị, cần người duyệt"
	case !has:
		res.Reason = fmt.Sprintf("Siêu thị %s chưa có thông tin trong dữ liệu đã nạp", d.StoreCode)
	case len(ips) == 0:
		res.Reason = fmt.Sprintf("Thông tin siêu thị %s chưa có IP WAN", d.StoreCode)
	case !slices.Contains(ips, ip.String()):
		res.Reason = fmt.Sprintf("Máy gửi từ IP %s, không thuộc IP WAN của siêu thị %s", ip, d.StoreCode)
	}
	if res.Reason == "" {
		for _, o := range f.devices {
			if o.ID != id && o.Status == DeviceApproved && (o.Hostname == d.Hostname || o.StoreCode == d.StoreCode && o.TN == d.TN) {
				res.Reason = "Trùng hostname / vị trí TN với máy đang hoạt động " + o.Hostname
			}
		}
	}
	if res.Reason != "" {
		d.AutoNote = res.Reason
		f.mu.Unlock()
		return res, nil
	}
	f.mu.Unlock()
	if _, err := f.Approve(ctx, id, AutoApprover, ""); err != nil {
		return AutoApproveResult{}, err
	}
	f.mu.Lock()
	d.AutoNote, d.DecidedAt = "", time.Now()
	f.mu.Unlock()
	res.Approved = true
	return res, nil
}

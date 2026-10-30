package app

import (
	"bytes"
	"context"
	"sync"
	"time"
)

// fakeRegistry: kho máy trong bộ nhớ, làm theo đúng quy tắc của bản PostgreSQL
// (internal/store) để test handler mà không cần DB.
type fakeRegistry struct {
	mu      sync.Mutex
	devices []*fakeDevice
	stores  map[string]string // mã siêu thị → IP WAN
	serials map[int64]string
}

type fakeDevice struct {
	Device
	pub []byte
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{stores: map[string]string{}, serials: map[int64]string{}}
}

func (f *fakeRegistry) Enroll(_ context.Context, r EnrollRequest) (EnrollResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		if bytes.Equal(d.pub, r.PublicKey) {
			d.LastSeen, d.LastIP = time.Now(), r.IP.String()
			if d.Status == DevicePending {
				d.Hostname, d.HostnameOK, d.HostnameError = r.Hostname, r.HostnameOK, r.HostnameError
				d.StoreCode, d.TN, d.Role = r.StoreCode, r.TN, r.Role
			}
			return EnrollResult{DeviceID: d.ID, Status: d.Status}, nil
		}
	}
	pending := 0
	for _, d := range f.devices {
		if d.Status == DevicePending {
			pending++
		}
	}
	if pending >= MaxPendingRequests {
		return EnrollResult{}, ErrTooManyPending
	}
	d := &fakeDevice{pub: r.PublicKey, Device: Device{
		ID: int64(len(f.devices) + 1), Hostname: r.Hostname, HostnameOK: r.HostnameOK, HostnameError: r.HostnameError,
		StoreCode: r.StoreCode, TN: r.TN, Role: r.Role, Status: DevicePending, KeyID: KeyID(r.PublicKey),
		Fingerprint: r.Fingerprint, OSVersion: r.OSVersion, AgentVersion: r.AgentVersion,
		EnrollIP: r.IP.String(), RequestedAt: time.Now(),
	}}
	f.devices = append(f.devices, d)
	return EnrollResult{DeviceID: d.ID, Status: DevicePending, New: true}, nil
}

func (f *fakeRegistry) SetCertSerial(_ context.Context, id int64, serial string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serials[id] = serial
	return nil
}

func (f *fakeRegistry) find(id int64) *fakeDevice {
	for _, d := range f.devices {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (f *fakeRegistry) ListDevices(_ context.Context, status string) ([]Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Device
	for _, d := range f.devices {
		if d.Status == status {
			dev := d.Device
			dev.StoreWANIP = f.stores[d.StoreCode]
			out = append(out, dev)
		}
	}
	return out, nil
}

func (f *fakeRegistry) Approve(_ context.Context, id int64, by, store string) (Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.find(id)
	switch {
	case d == nil:
		return Device{}, ErrNotFound
	case d.Status != DevicePending:
		return Device{}, ErrWrongState
	}
	if d.StoreCode == "" {
		d.StoreCode = store
	}
	if d.StoreCode == "" {
		return Device{}, ErrNeedStore
	}
	for _, o := range f.devices {
		if o.Status == DeviceApproved && (o.Hostname == d.Hostname || o.StoreCode == d.StoreCode && o.TN != 0 && o.TN == d.TN) {
			return Device{}, ErrConflict
		}
	}
	if f.stores[d.StoreCode] == "" {
		f.stores[d.StoreCode] = d.EnrollIP
	}
	d.Status, d.DecidedBy, d.DecidedAt = DeviceApproved, by, time.Now()
	return d.Device, nil
}

func (f *fakeRegistry) setStatus(id int64, by, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.find(id)
	switch {
	case d == nil:
		return ErrNotFound
	case d.Status != from:
		return ErrWrongState
	}
	d.Status, d.DecidedBy, d.DecidedAt = to, by, time.Now()
	return nil
}

func (f *fakeRegistry) Reject(_ context.Context, id int64, by string) error {
	return f.setStatus(id, by, DevicePending, DeviceRejected)
}

func (f *fakeRegistry) Revoke(_ context.Context, id int64, by string) error {
	return f.setStatus(id, by, DeviceApproved, DeviceRevoked)
}

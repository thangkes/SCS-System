package app

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/thangkes/SCS-System/server/internal/periods"
)

// fakeRegistry: kho máy trong bộ nhớ, làm theo đúng quy tắc của bản PostgreSQL
// (internal/store) để test handler mà không cần DB.
type fakeRegistry struct {
	mu       sync.Mutex
	devices  []*fakeDevice
	stores   map[string]string // mã siêu thị → IP WAN
	serials  map[int64]string
	alerts   []Alert
	releases []Release
	rollout  Rollout
	configs  map[string]ConfigEntry // scope + "/" + key
	// Lưu theo giai đoạn (fakeperiods_test.go).
	periods  []*periods.Period
	status   map[int64]fakeStatus
	changes  map[int64][]StatusChange
	auto     fakeAuto        // duyệt tự động (fakeauto_test.go)
	progress []ProgressPoint // tiến độ phát hành (fakefleet_test.go)
}

type fakeDevice struct {
	Device
	pub []byte
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{stores: map[string]string{}, serials: map[int64]string{}, configs: map[string]ConfigEntry{},
		status: map[int64]fakeStatus{}, changes: map[int64][]StatusChange{}}
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
	f.raise(d.ID, r.Hostname, r.StoreCode, "enroll_request", "medium")
	if !r.HostnameOK {
		f.raise(d.ID, r.Hostname, "", "hostname_invalid", "medium")
	}
	return EnrollResult{DeviceID: d.ID, Status: DevicePending, New: true}, nil
}

func (f *fakeRegistry) SetCertSerial(_ context.Context, id int64, serial string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serials[id] = serial
	return nil
}

func (f *fakeRegistry) AgentAuth(_ context.Context, id int64) (AgentAuth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.find(id)
	if d == nil {
		return AgentAuth{}, ErrNotFound
	}
	return AgentAuth{PublicKey: d.pub, Status: d.Status, CertSerial: f.serials[id]}, nil
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
			dev.Rules = f.status[d.ID].Rules
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
	for i := range f.alerts {
		if f.alerts[i].DeviceID == id && f.alerts[i].Kind == "enroll_request" && f.alerts[i].AckedAt.IsZero() {
			f.alerts[i].AckedAt, f.alerts[i].AckedBy = time.Now(), by
		}
	}
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

func (f *fakeRegistry) raise(deviceID int64, host, store, kind, sev string) {
	now := time.Now()
	f.alerts = append(f.alerts, Alert{ID: int64(len(f.alerts) + 1), DeviceID: deviceID, Hostname: host, StoreCode: store,
		Kind: kind, Severity: sev, CreatedAt: now, LastAt: now, Occurrences: 1, Detail: []byte(`{}`)})
}

func (f *fakeRegistry) ListAlerts(_ context.Context, includeAcked bool) ([]Alert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Alert
	for i := len(f.alerts) - 1; i >= 0; i-- {
		if includeAcked || f.alerts[i].AckedAt.IsZero() {
			out = append(out, f.alerts[i])
		}
	}
	return out, nil
}

func (f *fakeRegistry) AckAlert(_ context.Context, id int64, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.alerts {
		if f.alerts[i].ID == id {
			if !f.alerts[i].AckedAt.IsZero() {
				return ErrWrongState
			}
			if pid := f.alerts[i].PeriodID; pid != 0 {
				if err := f.resolveLocked(pid, by, ""); err == nil || !errors.Is(err, ErrWrongState) {
					return err
				}
			}
			f.alerts[i].AckedAt, f.alerts[i].AckedBy = time.Now(), by
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeRegistry) AddRelease(_ context.Context, r Release) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.releases {
		if x.Version == r.Version && x.Arch == r.Arch {
			return nil
		}
	}
	r.CreatedAt = time.Now()
	f.releases = append(f.releases, r)
	return nil
}

func (f *fakeRegistry) ListReleases(context.Context) ([]Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Release(nil), f.releases...), nil
}

func (f *fakeRegistry) GetRelease(_ context.Context, version, arch string) (Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.releases {
		if x.Version == version && x.Arch == arch {
			return x, nil
		}
	}
	return Release{}, ErrNotFound
}

func (f *fakeRegistry) GetRollout(context.Context) (Rollout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rollout, nil
}

func (f *fakeRegistry) SetRollout(_ context.Context, ring, version, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if version != "" {
		found := false
		for _, x := range f.releases {
			found = found || x.Version == version
		}
		if !found {
			return ErrNotFound
		}
	}
	switch ring {
	case RingTest:
		f.rollout.Test = version
	case RingAll:
		f.rollout.All = version
	default:
		return ErrNotFound
	}
	return nil
}

func (f *fakeRegistry) SetTestRing(_ context.Context, id int64, on bool, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.find(id)
	if d == nil {
		return ErrNotFound
	}
	d.TestRing = on
	return nil
}

func (f *fakeRegistry) AgentPlan(ctx context.Context, id int64, arch string) (AgentPlan, error) {
	f.mu.Lock()
	d := f.find(id)
	ro := f.rollout
	var watch []string
	if d != nil {
		for _, k := range []string{ScopeDevice + "/" + strconv.FormatInt(id, 10), ScopeStore + "/" + d.StoreCode, ScopeGlobal + "/"} {
			if c, ok := f.configs[k]; ok && c.WatchServices != nil {
				watch = c.WatchServices
				break
			}
		}
	}
	f.mu.Unlock()
	if d == nil {
		return AgentPlan{}, ErrNotFound
	}
	plan := AgentPlan{WatchServices: watch}
	if d != nil {
		f.mu.Lock()
		for _, k := range []string{ScopeDevice + "/" + strconv.FormatInt(id, 10), ScopeStore + "/" + d.StoreCode, ScopeGlobal + "/"} {
			if c, ok := f.configs[k]; ok && c.RuleSet != "" {
				plan.RuleSet = c.RuleSet
				break
			}
		}
		f.mu.Unlock()
	}
	if d != nil {
		for _, k := range []string{ScopeGlobal + "/", ScopeStore + "/" + d.StoreCode, ScopeDevice + "/" + strconv.FormatInt(id, 10)} {
			f.mu.Lock()
			c, ok := f.configs[k]
			f.mu.Unlock()
			for lk, lv := range c.Labels {
				if ok {
					if plan.Labels == nil {
						plan.Labels = map[string]string{}
					}
					plan.Labels[lk] = lv
				}
			}
		}
	}
	target := ro.All
	if d.TestRing && ro.Test != "" {
		target = ro.Test
	}
	if target != "" {
		if r, err := f.GetRelease(ctx, target, arch); err == nil {
			plan.Release = &r
		}
	}
	return plan, nil
}

func (f *fakeRegistry) ListConfigs(context.Context) ([]ConfigEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ConfigEntry
	for _, c := range f.configs {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeRegistry) SetConfig(_ context.Context, c ConfigEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := c.Scope + "/" + c.Key
	old := f.configs[k]
	c.Labels, c.RuleSet = old.Labels, old.RuleSet
	if c.WatchServices == nil && c.Labels == nil && c.RuleSet == "" {
		delete(f.configs, k)
	} else {
		c.UpdatedAt = time.Now()
		f.configs[k] = c
	}
	return nil
}

func (f *fakeRegistry) SetLabels(_ context.Context, c ConfigEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := c.Scope + "/" + c.Key
	old := f.configs[k]
	c.WatchServices, c.RuleSet = old.WatchServices, old.RuleSet
	if c.WatchServices == nil && c.Labels == nil && c.RuleSet == "" {
		delete(f.configs, k)
	} else {
		c.UpdatedAt = time.Now()
		f.configs[k] = c
	}
	return nil
}

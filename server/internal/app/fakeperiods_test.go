package app

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/thangkes/SCS-System/server/internal/periods"
)

// Bản trong bộ nhớ của phần lưu theo giai đoạn, làm đúng như store/periods.go.

type fakeStatus struct {
	At time.Time
	HeartbeatRecord
}

func (f *fakeRegistry) openPeriod(id int64) *periods.Period {
	for _, p := range f.periods {
		if p.DeviceID == id && p.Open() {
			return p
		}
	}
	return nil
}

func (f *fakeRegistry) alertOf(pid int64) *Alert {
	for i := range f.alerts {
		if f.alerts[i].PeriodID == pid && f.alerts[i].AckedAt.IsZero() {
			return &f.alerts[i]
		}
	}
	return nil
}

func (f *fakeRegistry) store(p *periods.Period) {
	for i, q := range f.periods {
		if q.ID == p.ID {
			f.periods[i] = p
			return
		}
	}
	p.ID = int64(len(f.periods) + 1)
	f.periods = append(f.periods, p)
}

func (f *fakeRegistry) apply(prev *periods.Period, r periods.Result, now time.Time) {
	if c := r.Closed; c != nil {
		f.store(c)
		if c.Kind == periods.KindAlert {
			if a := f.alertOf(c.ID); a != nil {
				a.AckedAt, a.AckedBy, a.Severity = now, c.ClosedBy, c.Severity
			}
		}
	}
	syncAlert := func(p *periods.Period) {
		if p.Kind != periods.KindAlert {
			return
		}
		if a := f.alertOf(p.ID); a != nil {
			a.Severity, a.Occurrences, a.LastAt = p.Severity, max(p.Events, 1), now
			return
		}
		f.alerts = append(f.alerts, Alert{ID: int64(len(f.alerts) + 1), DeviceID: p.DeviceID, Kind: "event",
			Severity: p.Severity, CreatedAt: p.StartedAt, LastAt: now, Occurrences: max(p.Events, 1),
			Detail: []byte(`{}`), PeriodID: p.ID})
	}
	if u := r.Updated; u != nil {
		f.store(u)
		syncAlert(u)
	}
	if o := r.Opened; o != nil {
		f.store(o)
		syncAlert(o)
	}
	dev := prev
	if dev == nil {
		dev = r.Current(prev)
	}
	switch {
	case dev == nil:
	case r.OfflineAlert > 0:
		f.raise(dev.DeviceID, "", "", "offline", "medium")
	case r.OfflineAlert < 0:
		for i := range f.alerts {
			if f.alerts[i].DeviceID == dev.DeviceID && f.alerts[i].Kind == "offline" && f.alerts[i].AckedAt.IsZero() {
				f.alerts[i].AckedAt, f.alerts[i].AckedBy = now, "máy gửi lại"
			}
		}
	}
}

func (f *fakeRegistry) RecordHeartbeat(_ context.Context, id int64, h HeartbeatRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if d := f.find(id); d != nil {
		d.LastSeen, d.LastIP, d.AgentVersion = now, h.IP.String(), h.AgentVersion
	}
	old, had := f.status[id]
	f.status[id] = fakeStatus{At: now, HeartbeatRecord: h}
	if h.Sig != "" && (!had || old.Sig != h.Sig) {
		b, _ := json.Marshal(h.Summary)
		f.changes[id] = append(f.changes[id], StatusChange{At: now, Changes: b})
	}
	cur := f.openPeriod(id)
	f.apply(cur, periods.Seen(cur, id, now), now)
	return nil
}

func (f *fakeRegistry) AddEvents(_ context.Context, id int64, evs []periods.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	cur := f.openPeriod(id)
	r := periods.Seen(cur, id, now)
	f.apply(cur, r, now)
	cur = f.openPeriod(id)
	f.apply(cur, periods.AddEvents(cur, id, evs, now), now)
	return nil
}

func (f *fakeRegistry) PeriodTick(_ context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, d := range f.devices {
		cur := f.openPeriod(d.ID)
		if cur == nil {
			continue
		}
		var r periods.Result
		if d.Status != DeviceApproved {
			r = periods.Retire(cur, now)
		} else {
			r = periods.Tick(cur, f.status[d.ID].At, now, periods.DefaultPolicy)
		}
		if !r.Empty() {
			n++
			f.apply(cur, r, now)
		}
	}
	return n, nil
}

func (f *fakeRegistry) ListPeriods(_ context.Context, id int64, limit int) (Timeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.find(id) == nil {
		return Timeline{}, ErrNotFound
	}
	tl := Timeline{LastSeen: f.status[id].At, Periods: []periods.Period{}, Changes: []StatusChange{}}
	for _, p := range f.periods {
		if p.DeviceID == id {
			tl.Periods = append(tl.Periods, *p)
		}
	}
	sort.Slice(tl.Periods, func(i, j int) bool { return tl.Periods[i].ID > tl.Periods[j].ID })
	for i := len(f.changes[id]) - 1; i >= 0; i-- {
		tl.Changes = append(tl.Changes, f.changes[id][i])
	}
	return tl, nil
}

func (f *fakeRegistry) ResolvePeriod(_ context.Context, pid int64, by, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolveLocked(pid, by, note)
}

func (f *fakeRegistry) resolveLocked(pid int64, by, note string) error {
	var p *periods.Period
	for _, q := range f.periods {
		if q.ID == pid {
			p = q
		}
	}
	if p == nil {
		return ErrNotFound
	}
	now := time.Now()
	r, err := periods.Resolve(p, by, note, f.status[p.DeviceID].At, now, periods.DefaultPolicy)
	if err != nil {
		return ErrWrongState
	}
	f.apply(p, r, now)
	return nil
}

func (f *fakeRegistry) Pulse(_ context.Context, since time.Time) (Pulse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := Pulse{Changed: []Alert{}}
	for _, a := range f.alerts {
		if a.LastAt.After(p.LastChange) {
			p.LastChange = a.LastAt
		}
		if a.AckedAt.After(p.LastChange) {
			p.LastChange = a.AckedAt
		}
		if !a.AckedAt.IsZero() {
			continue
		}
		p.OpenAlerts++
		if a.Severity == "high" || a.Severity == "critical" {
			p.Serious++
		}
		if a.Kind == "offline" {
			p.Offline++
		}
		if !since.IsZero() && a.LastAt.After(since) && len(p.Changed) < 20 {
			p.Changed = append(p.Changed, a)
		}
	}
	for _, d := range f.devices {
		if d.Status == DevicePending {
			p.Pending++
		}
	}
	return p, nil
}

func (f *fakeRegistry) RuleUsage(context.Context) ([]RuleUsage, int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := map[RuleUsage]int{}
	none, unknown := 0, 0
	for _, d := range f.devices {
		if d.Status != DeviceApproved {
			continue
		}
		r := f.status[d.ID].Rules
		switch {
		case r == nil:
			unknown++
		case !r.Installed:
			none++
		default:
			count[RuleUsage{Name: r.Name, Version: r.Version, SHA256: r.SHA256}]++
		}
	}
	out := []RuleUsage{}
	for u, n := range count {
		u.Devices = n
		out = append(out, u)
	}
	return out, none, unknown, nil
}

func (f *fakeRegistry) SetRuleSet(_ context.Context, c ConfigEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := c.Scope + "/" + c.Key
	old := f.configs[k]
	c.WatchServices, c.Labels = old.WatchServices, old.Labels
	if c.WatchServices == nil && c.Labels == nil && c.RuleSet == "" {
		delete(f.configs, k)
	} else {
		c.UpdatedAt = time.Now()
		f.configs[k] = c
	}
	return nil
}

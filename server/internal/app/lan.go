package app

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/common/rules"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/lan"
)

// Kiểm kê thiết bị LAN: nhận báo cáo kèm heartbeat, gộp theo siêu thị, trang web "Thiết bị LAN" (chỉ tham khảo).

// LANStore: phần lưu kiểm kê LAN (PostgreSQL). Tách khỏi Registry để kho giả trong test không phải cài.
type LANStore interface {
	// LANReport ghi báo cáo của một máy (rep.ReceivedAt = now) và gộp vào siêu thị; trả số lần đổi trạng thái đã ghi.
	LANReport(ctx context.Context, rep *lan.Reporter, m *rules.LAN, now time.Time) (int, error)
	LANData(ctx context.Context, store string) (LANData, error)
	LANChanges(ctx context.Context, store string, limit int) ([]lan.Change, error)
	PruneLAN(ctx context.Context, before time.Time) (int64, error)
}

// LANData: dữ liệu thô để dựng trang, theo mã siêu thị.
type LANData struct {
	Sites     map[string]*lan.Site
	Reporters map[string][]lan.Reporter
	Devices   map[string][]lan.Device
}

func lanStore(d Deps) (LANStore, bool) {
	ls, ok := d.Registry.(LANStore)
	return ls, ok && d.Rules != nil
}

// lanWarn: ghi log "báo cáo LAN bị bỏ" tối đa mỗi giờ một lần cho mỗi lý do.
var lanWarn = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

func lanWarnOnce(d Deps, why string, args ...any) {
	lanWarn.Lock()
	last, ok := lanWarn.at[why]
	if ok && time.Since(last) < time.Hour {
		lanWarn.Unlock()
		return
	}
	lanWarn.at[why] = time.Now()
	lanWarn.Unlock()
	d.Log.Warn("bỏ báo cáo thiết bị LAN: "+why, args...)
}

// ingestLAN: heartbeat có báo cáo LAN. Lỗi chỉ ghi log — heartbeat vẫn thành công (agent bỏ báo cáo, lượt sau gửi
// lượt mới). Thời điểm do server ghi (không tin giờ của agent).
func ingestLAN(ctx context.Context, d Deps, id int64, r *protocol.LANReport) {
	ls, ok := lanStore(d)
	if !ok || r == nil {
		return
	}
	m := d.Rules.LANMaps()[r.ListV]
	if m == nil {
		lanWarnOnce(d, "sơ đồ (list_v) không có trong các bộ rule đã ký của server này", "device", id, "list_v", r.ListV)
		return
	}
	up, skip, ip, err := lan.ParseReport(r, m)
	if err != nil {
		lanWarnOnce(d, "báo cáo sai dạng", "device", id, "err", err)
		return
	}
	durMs := r.DurMs
	if durMs < 0 || durMs > 3_600_000 {
		durMs = 0
	}
	now := time.Now().UTC()
	rep := &lan.Reporter{DeviceID: id, ListV: r.ListV, ReceivedAt: now, State: r.State, LANIP: ip, N: r.N,
		Up: up, Skip: skip, DurMs: durMs}
	if _, err := ls.LANReport(ctx, rep, m, now); err != nil {
		d.Log.Error("ghi báo cáo thiết bị LAN thất bại", "device", id, "err", err)
	}
}

// pruneLAN: xóa lịch sử thiết bị LAN quá 10 ngày (worker gọi mỗi giờ).
func pruneLAN(ctx context.Context, d Deps, now time.Time) {
	ls, ok := d.Registry.(LANStore)
	if !ok {
		return
	}
	if n, err := ls.PruneLAN(ctx, now.Add(-lan.Retention)); err != nil && ctx.Err() == nil {
		d.Log.Error("xóa lịch sử thiết bị LAN cũ thất bại", "err", err)
	} else if n > 0 {
		d.Log.Info("đã xóa lịch sử thiết bị LAN quá 10 ngày", "so_dong", n)
	}
}

type lanAPI struct{ d Deps }

// Bộ nhớ đệm trang danh sách (đọc mọi siêu thị khá nặng): 60 giây.
var lanCache = struct {
	sync.Mutex
	at   time.Time
	body map[string]any
}{}

const lanCacheTTL = time.Minute

// summary: GET /admin/v1/lan — mọi siêu thị có dữ liệu (mức Chỉ xem).
func (a lanAPI) summary(w http.ResponseWriter, r *http.Request) {
	ls, ok := lanStore(a.d)
	if !ok {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": false, "stores": []any{}})
		return
	}
	lanCache.Lock()
	defer lanCache.Unlock()
	if lanCache.body != nil && time.Since(lanCache.at) < lanCacheTTL {
		httpx.WriteJSON(w, http.StatusOK, lanCache.body)
		return
	}
	data, err := ls.LANData(r.Context(), "")
	if err != nil {
		a.d.Log.Error("đọc dữ liệu thiết bị LAN thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	maps := a.d.Rules.LANMaps()
	now := time.Now().UTC()
	out := make([]lan.Summary, 0, len(data.Sites))
	for code, site := range data.Sites {
		out = append(out, lan.Summarize(lan.View(site, data.Reporters[code], data.Devices[code], maps, now)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Store < out[j].Store })
	body := map[string]any{"ok": true, "enabled": true, "maps": len(maps), "allow": len(a.d.Cfg.LANAllow) > 0, "allow_err": a.d.Cfg.LANAllowErr,
		"stores": out, "at": now, "types": rules.LANTypes}
	lanCache.body, lanCache.at = body, time.Now()
	httpx.WriteJSON(w, http.StatusOK, body)
}

// store: GET /admin/v1/lan/{code} — chi tiết một siêu thị + lịch sử 10 ngày (mức Chỉ xem).
func (a lanAPI) store(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !storeCodeRe.MatchString(code) {
		httpx.WriteError(w, http.StatusBadRequest, "mã siêu thị không hợp lệ")
		return
	}
	ls, ok := lanStore(a.d)
	if !ok {
		httpx.WriteError(w, http.StatusServiceUnavailable, "chưa có dữ liệu thiết bị LAN")
		return
	}
	data, err := ls.LANData(r.Context(), code)
	if err != nil {
		a.d.Log.Error("đọc dữ liệu thiết bị LAN thất bại", "err", err, "store", code)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	site := data.Sites[code]
	if site == nil {
		httpx.WriteError(w, http.StatusNotFound, "siêu thị chưa có báo cáo thiết bị LAN")
		return
	}
	changes, err := ls.LANChanges(r.Context(), code, 500)
	if err != nil {
		a.d.Log.Error("đọc lịch sử thiết bị LAN thất bại", "err", err, "store", code)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	v := lan.View(site, data.Reporters[code], data.Devices[code], a.d.Rules.LANMaps(), time.Now().UTC())
	host := map[int64]string{}
	for _, dv := range data.Devices[code] {
		host[dv.ID] = dv.Hostname
	}
	type changeOut struct {
		lan.Change
		ByHost string `json:"by_host,omitempty"`
	}
	ch := make([]changeOut, 0, len(changes))
	for _, c := range changes {
		ch = append(ch, changeOut{Change: c, ByHost: host[c.By]})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "store": v, "changes": ch, "types": rules.LANTypes})
}

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/common/rules"
	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// releaseFile: đường dẫn file của một bản agent trong kho (SCS_AGENT_RELEASES/<phiên bản>/).
func releaseFile(root, version, arch string) string {
	return filepath.Join(root, version, "scs-agent-linux-"+arch)
}

// ImportAgentDist nhập các bản agent đi kèm bản server này (SCS_AGENT_DIST, do install.sh chép
// vào: file VERSION + scs-agent-linux-<arch>) vào kho và DB, ký bằng khóa phát hành. Chạy nền lúc
// khởi động; DB chưa sẵn sàng (đang cập nhật cấu trúc) thì thử lại tới khi được.
func ImportAgentDist(d Deps) func(context.Context) error {
	return func(ctx context.Context) error {
		if d.ReleaseKey == nil || d.Registry == nil {
			return nil
		}
		for wait := time.Second; ; wait = min(wait*2, time.Minute) {
			n, err := importDist(ctx, d)
			if err == nil {
				if n > 0 {
					d.Log.Info("đã nhập bản agent vào kho phát hành", "so_ban", n, "thu_muc", d.Cfg.AgentDist)
				}
				<-ctx.Done()
				return nil
			}
			d.Log.Warn("chưa nhập được bản agent, sẽ thử lại", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
		}
	}
}

func importDist(ctx context.Context, d Deps) (int, error) {
	vb, err := os.ReadFile(filepath.Join(d.Cfg.AgentDist, "VERSION"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil // bản server này không kèm agent
	}
	if err != nil {
		return 0, err
	}
	version := strings.TrimSpace(string(vb))
	if !protocol.ValidVersion(version) {
		return 0, fmt.Errorf("VERSION %q không hợp lệ", version)
	}
	n := 0
	for _, arch := range protocol.Arches {
		data, err := readLimited(filepath.Join(d.Cfg.AgentDist, "scs-agent-linux-"+arch), protocol.MaxAgentBinary)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return n, err
		}
		sum := sha256.Sum256(data)
		r := Release{Version: version, Arch: arch, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
		if old, err := d.Registry.GetRelease(ctx, version, arch); err == nil {
			if old.SHA256 != r.SHA256 {
				d.Log.Warn("bản agent cùng phiên bản nhưng khác nội dung, giữ bản đã phát hành", "version", version, "arch", arch)
			}
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return n, err
		}
		if err := writeAtomic(releaseFile(d.Cfg.AgentReleases, version, arch), data); err != nil {
			return n, err
		}
		r.Sig = protocol.SignRelease(d.ReleaseKey, version, arch, r.SHA256)
		if err := d.Registry.AddRelease(ctx, r); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s lớn quá %d MB", path, max>>20)
	}
	return data, nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// configOf: cấu hình gửi cho agent; Version là mã băm nội dung để agent báo lại bản đang dùng.
// watch nil = server không đặt dịch vụ theo dõi (agent dùng agent.env). Chưa có nhãn thì mã băm
// giữ như bản trước (agent đang chạy không phải nhận lại cấu hình).
func configOf(watch []string, labels map[string]string) protocol.AgentConfig {
	var b []byte
	if labels == nil {
		if watch == nil {
			watch = []string{}
		}
		b, _ = json.Marshal(watch)
	} else {
		b, _ = json.Marshal(struct {
			W []string          `json:"w"`
			L map[string]string `json:"l"`
		}{watch, labels}) // json sắp xếp khóa map nên cùng nội dung ra cùng mã băm
	}
	sum := sha256.Sum256(b)
	return protocol.AgentConfig{Version: hex.EncodeToString(sum[:8]), WatchServices: watch, Labels: labels}
}

// maxLabels: số nhãn tối đa ở một cấp.
const maxLabels = 16

// setLabels: POST /admin/v1/agent/labels {"scope", "key", "labels": {"role": "pri", ...} | null}.
// null = bỏ nhãn ở cấp này. Nhãn ghi đè ngữ cảnh agent tự suy ra (role / store từ hostname,
// os từ /etc/os-release) để chọn rule theo applies_to.
func (a agentAPI) setLabels(w http.ResponseWriter, r *http.Request) {
	if !(deviceAPI{a.d}).ready(w) {
		return
	}
	var c ConfigEntry
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&c); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	switch {
	case c.Scope == ScopeGlobal && c.Key == "":
	case c.Scope == ScopeStore && storeCodeRe.MatchString(c.Key):
	case c.Scope == ScopeDevice && deviceKeyRe.MatchString(c.Key):
	default:
		httpx.WriteError(w, http.StatusBadRequest, "cấp cấu hình không hợp lệ")
		return
	}
	if c.Labels != nil && len(c.Labels) == 0 {
		c.Labels = nil
	}
	if len(c.Labels) > maxLabels {
		httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("tối đa %d nhãn", maxLabels))
		return
	}
	for k, v := range c.Labels {
		if !rules.ValidContextKey(k) || !rules.ValidContextValue(v) {
			httpx.WriteError(w, http.StatusBadRequest,
				fmt.Sprintf("nhãn %q=%q: khóa chữ thường/số/_, giá trị chữ thường/số/. _ - (ví dụ role=pri, os=ubuntu-22.04)", k, v))
			return
		}
	}
	c.WatchServices = nil
	c.UpdatedBy = actor(r)
	if (deviceAPI{a.d}).fail(w, a.d.Registry.SetLabels(r.Context(), c), "lưu nhãn máy") {
		return
	}
	a.d.Log.Info("đổi nhãn máy", "by", c.UpdatedBy, "cap", c.Scope, "khoa", c.Key, "nhan", c.Labels)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// updateHandler: GET /v1/updates/{version}?arch=amd64 — máy đã duyệt tải bản agent.
// Agent kiểm SHA-256 và chữ ký phát hành trước khi chạy, nên ở đây chỉ cần đúng máy.
func updateHandler(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := authDevice(d, w, r)
		if !ok {
			return
		}
		version, arch := r.PathValue("version"), r.URL.Query().Get("arch")
		if !protocol.ValidVersion(version) || !protocol.ValidArch(arch) {
			httpx.WriteError(w, http.StatusBadRequest, "phiên bản / kiến trúc không hợp lệ")
			return
		}
		rel, err := d.Registry.GetRelease(r.Context(), version, arch)
		if errors.Is(err, ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "không có bản agent này")
			return
		}
		if err != nil {
			d.Log.Error("đọc bản agent thất bại", "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		f, err := os.Open(releaseFile(d.Cfg.AgentReleases, version, arch))
		if err != nil {
			d.Log.Error("thiếu file bản agent trong kho", "err", err, "version", version, "arch", arch)
			httpx.WriteError(w, http.StatusNotFound, "không có bản agent này")
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.Size() != rel.Size {
			d.Log.Error("file bản agent trong kho sai kích thước", "version", version, "arch", arch)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		// Chặn dự phòng: máy tải mà không giữ lượt (agent cũ thử lại, hay tải tay) khi đã hết lượt.
		if !d.Downloads.Acquire(id) {
			w.Header().Set("Retry-After", "600")
			httpx.WriteError(w, http.StatusServiceUnavailable, "đang hết lượt tải bản agent, thử lại ở heartbeat sau")
			return
		}
		d.Log.Info("máy tải bản agent", "device", id, "version", version, "arch", arch)
		w.Header().Set("Content-Type", "application/octet-stream")
		tw := d.Downloads.throttle(w, r)
		http.ServeContent(tw, r, "", st.ModTime(), f)
		complete := r.Context().Err() == nil
		if t, ok := tw.(*throttledWriter); ok && r.Method == http.MethodGet && r.Header.Get("Range") == "" {
			complete = complete && t.written == st.Size()
		}
		d.Downloads.Release(id, complete)
	})
}

// ---------------------------------------------------------------- API web quản trị

type agentAPI struct{ d Deps }

// Tên dịch vụ systemd hợp lệ (giống phía agent); không bắt đầu bằng "-".
var serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9@_.:\\][A-Za-z0-9@_.:\\-]{0,127}$`)

var deviceKeyRe = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

// releaseItem: một phiên bản trong kho (gộp các kiến trúc).
type releaseItem struct {
	Version   string    `json:"version"`
	Arches    []string  `json:"arches"`
	CreatedAt time.Time `json:"created_at"`
}

// releases: GET /admin/v1/agent/releases — các bản trong kho và bản đang phát hành cho từng nhóm.
func (a agentAPI) releases(w http.ResponseWriter, r *http.Request) {
	if !(deviceAPI{a.d}).ready(w) {
		return
	}
	list, err := a.d.Registry.ListReleases(r.Context())
	var ro Rollout
	if err == nil {
		ro, err = a.d.Registry.GetRollout(r.Context())
	}
	if err != nil {
		a.d.Log.Error("đọc bản agent thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	out := []releaseItem{}
	for _, rel := range list {
		i := slices.IndexFunc(out, func(x releaseItem) bool { return x.Version == rel.Version })
		if i < 0 {
			out = append(out, releaseItem{Version: rel.Version, CreatedAt: rel.CreatedAt})
			i = len(out) - 1
		}
		out[i].Arches = append(out[i].Arches, rel.Arch)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "releases": out, "rollout": ro,
		"signing": a.d.ReleaseKey != nil})
}

// rollout: POST /admin/v1/agent/rollout {"ring": "test"|"all", "version": "..." | ""}.
func (a agentAPI) rollout(w http.ResponseWriter, r *http.Request) {
	if !(deviceAPI{a.d}).ready(w) {
		return
	}
	var body struct {
		Ring    string `json:"ring"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil ||
		(body.Ring != RingTest && body.Ring != RingAll) || (body.Version != "" && !protocol.ValidVersion(body.Version)) {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	by := actor(r)
	err := a.d.Registry.SetRollout(r.Context(), body.Ring, body.Version, by)
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "không có bản agent này trong kho")
		return
	}
	if (deviceAPI{a.d}).fail(w, err, "phát hành bản agent") {
		return
	}
	a.d.Log.Warn("đổi bản agent phát hành", "by", by, "nhom", body.Ring, "version", body.Version)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// testRing: POST /admin/v1/devices/{id}/test-ring {"on": true|false}.
func (a agentAPI) testRing(w http.ResponseWriter, r *http.Request) {
	dev := deviceAPI{a.d}
	id, ok := dev.id(w, r)
	if !ok {
		return
	}
	var body struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	if dev.fail(w, a.d.Registry.SetTestRing(r.Context(), id, body.On, actor(r)), "đổi nhóm thử") {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// configs: GET /admin/v1/agent/config — cấu hình agent ở mọi cấp.
func (a agentAPI) configs(w http.ResponseWriter, r *http.Request) {
	if !(deviceAPI{a.d}).ready(w) {
		return
	}
	list, err := a.d.Registry.ListConfigs(r.Context())
	if err != nil {
		a.d.Log.Error("đọc cấu hình agent thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	if list == nil {
		list = []ConfigEntry{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "configs": list})
}

// setConfig: POST /admin/v1/agent/config {"scope", "key", "watch_services": [...] | null}.
// null = bỏ ghi đè ở cấp này (kế thừa cấp trên).
func (a agentAPI) setConfig(w http.ResponseWriter, r *http.Request) {
	if !(deviceAPI{a.d}).ready(w) {
		return
	}
	var c ConfigEntry
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&c); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	switch {
	case c.Scope == ScopeGlobal && c.Key == "":
	case c.Scope == ScopeStore && storeCodeRe.MatchString(c.Key):
	case c.Scope == ScopeDevice && deviceKeyRe.MatchString(c.Key):
	default:
		httpx.WriteError(w, http.StatusBadRequest, "cấp cấu hình không hợp lệ")
		return
	}
	if len(c.WatchServices) > protocol.MaxServices {
		httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("tối đa %d dịch vụ", protocol.MaxServices))
		return
	}
	for _, s := range c.WatchServices {
		if !serviceNameRe.MatchString(s) {
			httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("%q không phải tên dịch vụ systemd hợp lệ", s))
			return
		}
	}
	c.UpdatedBy = actor(r)
	if (deviceAPI{a.d}).fail(w, a.d.Registry.SetConfig(r.Context(), c), "lưu cấu hình agent") {
		return
	}
	a.d.Log.Info("đổi cấu hình agent", "by", c.UpdatedBy, "cap", c.Scope, "khoa", c.Key, "dich_vu", c.WatchServices)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

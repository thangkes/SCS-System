package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/thangkes/SCS-System/common/rules"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
)

// Phát bộ rule cho agent (mốc M7): admin gán bộ đã ký theo cấp chung / siêu thị / máy; heartbeat trả
// "tên@phiên bản" bộ máy cần chạy (Response.RuleSetVersion); agent khác bộ đang cài thì tải
// GET /v1/rules/{tên@phiên bản}, kiểm chữ ký bằng release.pub rồi cài. Server chỉ phát bộ có bản ký hợp lệ
// trong kho, và chỉ cho máy tải đúng bộ đang gán cho nó.

var ruleSetRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}@[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

func splitRuleSet(v string) (string, string, bool) {
	if !ruleSetRe.MatchString(v) || strings.Contains(v, "..") {
		return "", "", false
	}
	i := strings.LastIndex(v, "@")
	return v[:i], v[i+1:], true
}

// ruleBundleFor: bản ký của bộ v trong kho, kiểm lại chữ ký bằng khóa phát hành của server.
func ruleBundleFor(d Deps, v string) ([]byte, error) {
	name, ver, ok := splitRuleSet(v)
	if !ok || d.Rules == nil {
		return nil, rulestore.ErrNotFound
	}
	b, err := d.Rules.Bundle(name, ver)
	if err != nil {
		return nil, err
	}
	if d.Rules.Pub != nil {
		var bd rules.Bundle
		if json.Unmarshal(b, &bd) != nil {
			return nil, errors.New("bản ký hỏng")
		}
		if _, err := bd.Open(d.Rules.Pub); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func hasCap(caps []string, prefix string) bool {
	for _, c := range caps {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// rulesHandler: GET /v1/rules/{version} — máy đã duyệt tải bộ rule đang gán cho nó.
func rulesHandler(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := authDevice(d, w, r)
		if !ok {
			return
		}
		v := r.PathValue("version")
		if _, _, ok := splitRuleSet(v); !ok {
			httpx.WriteError(w, http.StatusBadRequest, "tên bộ rule không hợp lệ")
			return
		}
		plan, err := d.Registry.AgentPlan(r.Context(), id, "")
		if err != nil {
			d.Log.Error("đọc bộ rule gán cho máy thất bại", "err", err, "device", id)
			httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
			return
		}
		if plan.RuleSet != v {
			httpx.WriteError(w, http.StatusNotFound, "bộ rule này không gán cho máy")
			return
		}
		b, err := ruleBundleFor(d, v)
		if err != nil {
			d.Log.Error("không phát được bộ rule", "err", err, "bo", v)
			httpx.WriteError(w, http.StatusNotFound, "chưa có bản ký hợp lệ của bộ rule này")
			return
		}
		d.Log.Info("máy tải bộ rule", "device", id, "bo", v)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
}

// assign: POST /admin/v1/rules/assign {scope, key, rule_set} — gán bộ rule đã ký cho cấp chung /
// siêu thị / máy (rule_set rỗng = bỏ gán ở cấp này).
func (a rulesAPI) assign(w http.ResponseWriter, r *http.Request) {
	if !(deviceAPI{a.d}).ready(w) {
		return
	}
	var c ConfigEntry
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&c); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	switch {
	case c.Scope == ScopeGlobal && c.Key == "":
	case c.Scope == ScopeStore && storeCodeRe.MatchString(c.Key):
	case c.Scope == ScopeDevice && deviceKeyRe.MatchString(c.Key):
	default:
		httpx.WriteError(w, http.StatusBadRequest, "cấp không hợp lệ")
		return
	}
	if c.RuleSet != "" {
		if _, err := ruleBundleFor(a.d, c.RuleSet); err != nil {
			httpx.WriteError(w, http.StatusConflict, fmt.Sprintf("%s chưa có bản ký hợp lệ trong kho: duyệt và ký trước", c.RuleSet))
			return
		}
	}
	c.WatchServices, c.Labels = nil, nil
	c.UpdatedBy = actor(r)
	if (deviceAPI{a.d}).fail(w, a.d.Registry.SetRuleSet(r.Context(), c), "gán bộ rule") {
		return
	}
	a.d.Log.Warn("admin gán bộ rule", "cap", c.Scope, "khoa", c.Key, "bo", c.RuleSet, "boi", c.UpdatedBy)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// assignments: GET /admin/v1/rules/assign — các cấp đang gán bộ rule.
func (a rulesAPI) assignments(w http.ResponseWriter, r *http.Request) {
	if !(deviceAPI{a.d}).ready(w) {
		return
	}
	list, err := a.d.Registry.ListConfigs(r.Context())
	if err != nil {
		a.d.Log.Error("đọc cấu hình thất bại", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "lỗi nội bộ")
		return
	}
	out := []ConfigEntry{}
	for _, c := range list {
		if c.RuleSet != "" {
			out = append(out, ConfigEntry{Scope: c.Scope, Key: c.Key, RuleSet: c.RuleSet, UpdatedAt: c.UpdatedAt, UpdatedBy: c.UpdatedBy})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "assignments": out})
}

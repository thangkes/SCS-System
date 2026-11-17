package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
	"github.com/thangkes/SCS-System/server/internal/cluster"
	"github.com/thangkes/SCS-System/server/internal/httpx"
	"github.com/thangkes/SCS-System/server/internal/rulefile"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
)

// rulesAPI: trang "Rule quét log" — kho bộ rule trên server, tự chia chưa ký / đã ký / lỗi, admin
// duyệt (ký) từng bộ.
//
// Server đơn: duyệt = ký ngay bằng khóa phát hành của server.
// Server trong cụm: duyệt = đưa quyết định "ruleset.publish" (tên, phiên bản, SHA-256) vào cụm; chỉ
// khi quá nửa node ký thì server mới ký bộ rule cho agent của nó. Server khác trong cụm thêm đúng
// file đó (cùng SHA) rồi bấm duyệt là ký luôn, không cần bỏ phiếu lại. Cùng tên + phiên bản mà nội
// dung khác thì bị chặn: muốn sửa phải tăng version.
type rulesAPI struct{ d Deps }

// ruleProposeTimeout: thời gian chờ các node trong cụm ký đề nghị duyệt bộ rule.
const ruleProposeTimeout = 30 * time.Second

func (a rulesAPI) store(w http.ResponseWriter) *rulestore.Store {
	if a.d.Rules == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "kho bộ rule chưa mở được (xem log scs-server, biến SCS_RULES_DIR)")
	}
	return a.d.Rules
}

// cluster: node cụm nếu server đã thuộc một cụm.
func (a rulesAPI) cluster() *cluster.Node {
	if n := a.d.Cluster; n != nil && n.Initialized() {
		return n
	}
	return nil
}

func (a rulesAPI) approvals() (map[string]rulestore.Approval, error) {
	n := a.cluster()
	if n == nil {
		return nil, nil
	}
	log, err := cluster.ReadLog(n.Dir)
	if err != nil {
		return nil, err
	}
	return rulestore.Approvals(log), nil
}

// conflict: cụm đã duyệt tên + phiên bản này với nội dung khác.
func conflictWith(ap map[string]rulestore.Approval) rulestore.Conflict {
	return func(name, version, sha string) error {
		if x, ok := ap[rulestore.Key(name, version)]; ok && x.SHA256 != sha {
			return fmt.Errorf("%w: cụm đã duyệt %s với nội dung khác (quyết định #%d); đổi version",
				rulestore.ErrConflict, rulestore.Key(name, version), x.Seq)
		}
		return nil
	}
}

func ruleErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, rulestore.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, rulestore.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	default:
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	}
}

// list: GET /admin/v1/rules.
func (a rulesAPI) list(w http.ResponseWriter, r *http.Request) {
	s := a.store(w)
	if s == nil {
		return
	}
	entries, err := s.List()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "không đọc được kho bộ rule: "+err.Error())
		return
	}
	if entries == nil {
		entries = []rulestore.Entry{}
	}
	ap, aerr := a.approvals()
	have := map[string]bool{}
	for i := range entries {
		k := rulestore.Key(entries[i].Name, entries[i].Version)
		have[k] = true
		if x, ok := ap[k]; ok {
			x := x
			entries[i].Cluster = &x
		}
	}
	// Bộ cụm đã duyệt mà server này chưa có file: admin thêm đúng file đó vào là ký được ngay.
	type missing struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		rulestore.Approval
	}
	only := []missing{}
	for k, x := range ap {
		if have[k] {
			continue
		}
		var p cc.RulesetPayload
		for i := len(k) - 1; i >= 0; i-- {
			if k[i] == '@' {
				p.Name, p.Version = k[:i], k[i+1:]
				break
			}
		}
		only = append(only, missing{Name: p.Name, Version: p.Version, Approval: x})
	}
	out := map[string]any{"ok": true, "dir": s.Dir, "signing": a.d.ReleaseKey != nil, "entries": entries, "cluster_only": only}
	// Máy đang cài bộ nào (agent báo trong heartbeat): bộ khớp tên + phiên bản + SHA với kho thì
	// đếm vào bộ đó; còn lại (bản ký khác, bộ không có trong kho) liệt kê riêng.
	if a.d.Registry != nil {
		usage, none, unknown, err := a.d.Registry.RuleUsage(r.Context())
		if err != nil {
			a.d.Log.Warn("đọc bộ rule máy đang cài thất bại", "err", err)
		} else {
			used := map[int]int{}
			other := []RuleUsage{}
			for _, u := range usage {
				matched := false
				for i, e := range entries {
					if e.Name == u.Name && e.Version == u.Version && e.SHA256 == u.SHA256 && e.SHA256 != "" {
						used[i] += u.Devices
						matched = true
						break
					}
				}
				if !matched {
					other = append(other, u)
				}
			}
			counts := make([]int, len(entries))
			for i := range entries {
				counts[i] = used[i]
			}
			out["devices"] = counts
			out["other_usage"] = other
			out["no_rules"], out["unknown_rules"] = none, unknown
		}
	}
	if n := a.cluster(); n != nil {
		st := n.State()
		info := map[string]any{"name": st.Cluster, "valid": len(st.Valid()), "majority": cc.Majority(len(st.Valid()))}
		if aerr != nil {
			info["error"] = aerr.Error()
		}
		out["cluster"] = info
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// add: POST /admin/v1/rules, body là nội dung file YAML.
func (a rulesAPI) add(w http.ResponseWriter, r *http.Request) {
	s := a.store(w)
	if s == nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, rulefile.MaxFile+1))
	if err != nil || len(data) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "thiếu nội dung file rule")
		return
	}
	if len(data) > rulefile.MaxFile {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file rule tối đa %d byte", rulefile.MaxFile))
		return
	}
	ap, err := a.approvals()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "không đọc được nhật ký cụm: "+err.Error())
		return
	}
	e, err := s.Add(data, conflictWith(ap))
	if err != nil {
		ruleErr(w, err)
		return
	}
	a.d.Log.Warn("admin thêm bộ rule vào kho", "bo", rulestore.Key(e.Name, e.Version), "file", e.File,
		"sha256", e.SHA256, "trang_thai", e.Status, "boi", actor(r))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "entry": e})
}

type ruleReq struct {
	File    string `json:"file"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Bundle  bool   `json:"bundle"` // remove: xóa luôn bản ký
}

func readRuleReq(w http.ResponseWriter, r *http.Request) (ruleReq, bool) {
	var req ruleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "yêu cầu sai định dạng")
		return req, false
	}
	return req, true
}

// approve: POST /admin/v1/rules/approve {file}: duyệt rồi ký (trong cụm: quá nửa node duyệt trước).
func (a rulesAPI) approve(w http.ResponseWriter, r *http.Request) {
	s := a.store(w)
	if s == nil {
		return
	}
	req, ok := readRuleReq(w, r)
	if !ok {
		return
	}
	if a.d.ReleaseKey == nil {
		httpx.WriteError(w, http.StatusConflict, "server chưa có khóa phát hành (SCS_RELEASE_KEY): không ký được bộ rule")
		return
	}
	set, sha, err := s.Prepare(req.File)
	if err != nil {
		ruleErr(w, err)
		return
	}
	var seq uint64
	want := ""
	if n := a.cluster(); n != nil {
		ap, err := a.approvals()
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "không đọc được nhật ký cụm: "+err.Error())
			return
		}
		key := rulestore.Key(set.Name, set.Version)
		if x, ok := ap[key]; ok {
			if x.SHA256 != sha {
				ruleErr(w, conflictWith(ap)(set.Name, set.Version, sha))
				return
			}
			seq = x.Seq
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), ruleProposeTimeout)
			d, err := n.Propose(ctx, cc.TypeRulesetPublish,
				cc.RulesetPayload{Name: set.Name, Version: set.Version, SHA256: sha}, actor(r))
			cancel()
			if err != nil {
				httpx.WriteError(w, http.StatusConflict, "cụm chưa duyệt bộ rule (cần quá nửa server ký): "+err.Error())
				return
			}
			seq = d.Seq
		}
		want = sha
	}
	e, err := s.Sign(req.File, a.d.ReleaseKey, actor(r), seq, want)
	if err != nil {
		ruleErr(w, err)
		return
	}
	a.d.Log.Warn("admin duyệt và ký bộ rule", "bo", rulestore.Key(e.Name, e.Version), "sha256", e.SHA256,
		"quyet_dinh_cum", seq, "boi", actor(r))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "entry": e, "cluster_seq": seq})
}

// remove: POST /admin/v1/rules/remove {file, bundle} hoặc {name, version} (chỉ còn bản ký).
func (a rulesAPI) remove(w http.ResponseWriter, r *http.Request) {
	s := a.store(w)
	if s == nil {
		return
	}
	req, ok := readRuleReq(w, r)
	if !ok {
		return
	}
	var err error
	if req.File != "" {
		err = s.Remove(req.File, req.Bundle)
	} else {
		err = s.RemoveBundle(req.Name, req.Version)
	}
	if err != nil {
		ruleErr(w, err)
		return
	}
	a.d.Log.Warn("admin xóa bộ rule khỏi kho", "file", req.File, "bo", rulestore.Key(req.Name, req.Version),
		"xoa_ban_ky", req.Bundle || req.File == "", "boi", actor(r))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// source: GET /admin/v1/rules/source?file=: nội dung file nguồn (xem trên web).
func (a rulesAPI) source(w http.ResponseWriter, r *http.Request) {
	s := a.store(w)
	if s == nil {
		return
	}
	b, err := s.Source(r.URL.Query().Get("file"))
	if err != nil {
		ruleErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// bundle: GET /admin/v1/rules/bundle?name=&version=: tải bộ rule đã ký (chép sang agent).
func (a rulesAPI) bundle(w http.ResponseWriter, r *http.Request) {
	s := a.store(w)
	if s == nil {
		return
	}
	name, ver := r.URL.Query().Get("name"), r.URL.Query().Get("version")
	b, err := s.Bundle(name, ver)
	if err != nil {
		ruleErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", rulestore.Key(name, ver)+".json"))
	_, _ = w.Write(b)
}

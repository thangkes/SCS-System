package app

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
	"github.com/thangkes/SCS-System/server/internal/cluster"
	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// clusterAPI: API cho trang "Cụm server" trên web quản trị.
//
// Mọi việc làm đổi danh sách cụm (thêm, cô lập, bỏ cô lập, gỡ node) ở đây chỉ là XÁC NHẬN CỦA
// ADMIN TRÊN NODE NÀY. Quyết định chỉ có hiệu lực khi đủ số node cũng được xác nhận (thêm node:
// mọi node; còn lại: quá nửa), và admin phải đăng nhập từng node bằng mã của chính node đó.
type clusterAPI struct{ d Deps }

type clusterMember struct {
	cc.Member
	Fingerprint   string `json:"fingerprint"`
	Self          bool   `json:"self"`
	IsolatedLocal bool   `json:"isolated_local"`
}

type clusterJoin struct {
	cluster.JoinRequest
	Fingerprint string `json:"fingerprint"`
	Approved    bool   `json:"approved"` // admin đã xác nhận trên node này
}

func fingerprintOf(m cc.Member) string {
	pub, err := cc.DecodePub(m.PubKey)
	if err != nil {
		return ""
	}
	return cluster.KeyFingerprint(pub)
}

// status: GET /admin/v1/cluster.
func (a clusterAPI) status(w http.ResponseWriter, r *http.Request) {
	n := a.d.Cluster
	if n == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": false})
		return
	}
	st := n.State()
	local := n.Local()
	out := map[string]any{
		"ok": true, "enabled": true, "initialized": n.Initialized(),
		"node_id": n.ID, "fingerprint": cluster.KeyFingerprint(n.Key.Public().(ed25519.PublicKey)),
		"cluster": st.Cluster, "cluster_id": st.ClusterID, "seq": st.Seq, "epoch": st.Epoch,
		"valid": len(st.Valid()), "majority": cc.Majority(len(st.Valid())),
		"approvals": local.Approvals, "fork": n.Fork(), "equivocators": n.Equivocators(),
		"removed": st.Removed, "joining": local.Joining,
	}
	members := []clusterMember{}
	for _, m := range st.Members {
		_, iso := local.Isolated[m.ID]
		members = append(members, clusterMember{Member: m, Fingerprint: fingerprintOf(m), Self: m.ID == n.ID, IsolatedLocal: iso})
	}
	joins := []clusterJoin{}
	for _, j := range local.Joins {
		_, ok := local.Approvals[cc.TypeMemberAdd+":"+j.Member.ID]
		joins = append(joins, clusterJoin{JoinRequest: j, Fingerprint: fingerprintOf(j.Member), Approved: ok})
	}
	out["members"], out["joins"] = members, joins
	httpx.WriteJSON(w, http.StatusOK, out)
}

type clusterReq struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	On   bool   `json:"on"`
}

func (a clusterAPI) read(w http.ResponseWriter, r *http.Request) (*cluster.Node, clusterReq, bool) {
	var req clusterReq
	n := a.d.Cluster
	if n == nil || !n.Initialized() {
		httpx.WriteError(w, http.StatusConflict, "node này chưa thuộc cụm nào")
		return nil, req, false
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "thiếu mã node")
		return nil, req, false
	}
	return n, req, true
}

// approve: POST /admin/v1/cluster/approve {type, id}. Ghi xác nhận trên node này rồi thử đưa
// quyết định vào cụm; chưa đủ node xác nhận thì báo còn chờ node nào.
func (a clusterAPI) approve(w http.ResponseWriter, r *http.Request) {
	n, req, ok := a.read(w, r)
	if !ok {
		return
	}
	if err := n.Approve(req.Type, req.ID); err != nil {
		httpx.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	a.d.Log.Warn("admin xác nhận quyết định về danh sách cụm trên node này", "loai", req.Type, "node", req.ID, "boi", actor(r))
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	_, err := n.ProposeMember(ctx, req.Type, req.ID, "", actor(r))
	switch {
	case err == nil:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "done": true, "message": "Đã đủ xác nhận, quyết định có hiệu lực."})
	case errors.Is(err, cluster.ErrPending):
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "done": false, "message": "Đã ghi xác nhận trên node này. " + err.Error()})
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "done": false,
			"message": "Đã ghi xác nhận trên node này, nhưng chưa đưa được quyết định vào cụm (server sẽ tự thử lại): " + err.Error()})
	}
}

// unapprove: POST /admin/v1/cluster/unapprove {type, id}: rút lại xác nhận chưa thành.
func (a clusterAPI) unapprove(w http.ResponseWriter, r *http.Request) {
	n, req, ok := a.read(w, r)
	if !ok {
		return
	}
	if err := n.Unapprove(req.Type, req.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "không lưu được")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// rejectJoin: POST /admin/v1/cluster/reject-join {id}: bỏ yêu cầu xin vào cụm trên node này.
func (a clusterAPI) rejectJoin(w http.ResponseWriter, r *http.Request) {
	n, req, ok := a.read(w, r)
	if !ok {
		return
	}
	if err := n.RejectJoin(req.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "không lưu được")
		return
	}
	a.d.Log.Warn("admin từ chối yêu cầu xin vào cụm", "node", req.ID, "boi", actor(r))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// isolateLocal: POST /admin/v1/cluster/isolate-local {id, on}: node này ngừng / nối lại liên lạc
// với node id ngay lập tức (không cần đồng thuận).
func (a clusterAPI) isolateLocal(w http.ResponseWriter, r *http.Request) {
	n, req, ok := a.read(w, r)
	if !ok {
		return
	}
	if err := n.IsolateLocal(req.ID, req.On); err != nil {
		httpx.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	a.d.Log.Warn("admin đổi cô lập tại chỗ", "node", req.ID, "co_lap", req.On, "boi", actor(r))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

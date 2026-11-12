package cluster

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// Hello: thông tin công khai của node, trả cho mọi bên gọi có chứng chỉ (kể cả node chưa
// thuộc cụm, để admin đối chiếu mã khóa trước khi xin vào cụm).
type Hello struct {
	NodeID      string `json:"node_id"`
	Fingerprint string `json:"fingerprint"`
	ClusterID   string `json:"cluster_id,omitempty"` // 16 ký tự đầu
	Member      bool   `json:"member"`               // bên gọi có phải node hợp lệ của cụm không
	Version     string `json:"version,omitempty"`    // chỉ trả cho node hợp lệ (kẻ dò quét không biết bản đang chạy)
}

// Status: trạng thái cụm, chỉ trả cho node hợp lệ.
type Status struct {
	NodeID string    `json:"node_id"`
	State  cc.State  `json:"state"`
	Time   time.Time `json:"time"`
	// Fork: khác rỗng khi node thấy nhật ký bị tách nhánh. Equivocators: node đã ký hai đề nghị trái nhau.
	Fork         string            `json:"fork,omitempty"`
	Equivocators map[string]string `json:"equivocators,omitempty"`
}

// Handler: các đường dẫn trên cổng 5222.
func (n *Node) Handler(version string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /cluster/v1/hello", func(w http.ResponseWriter, r *http.Request) {
		_, _, member := n.Peer(r)
		st := n.State()
		h := Hello{NodeID: n.ID, Fingerprint: KeyFingerprint(n.Key.Public().(ed25519.PublicKey)),
			ClusterID: Short(st.ClusterID), Member: member}
		if member {
			h.Version = version
		}
		httpx.WriteJSON(w, http.StatusOK, h)
	})
	mux.Handle("GET /cluster/v1/status", n.membersOnly(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, Status{NodeID: n.ID, State: n.State(), Time: time.Now().UTC(),
			Fork: n.Fork(), Equivocators: n.Equivocators()})
	}))
	n.consensusRoutes(mux)
	n.joinRoutes(mux, log)
	return httpx.Chain(mux, httpx.SecureHeaders, httpx.Recover(log), httpx.MaxBody(4<<20),
		httpx.AggregatedAccessLog(log, slog.LevelDebug), httpx.WithRequestID)
}

// membersOnly: chỉ node hợp lệ trong danh sách cụm mới gọi được.
func (n *Node) membersOnly(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _, ok := n.Peer(r)
		if !ok {
			httpx.WriteError(w, http.StatusForbidden, "node "+id+" không có trong danh sách cụm")
			return
		}
		h(w, r)
	})
}

// Server: chạy cổng 5222 cho tới khi ctx bị hủy. Chưa thuộc cụm thì không mở cổng.
func (n *Node) Server(addr, version string, log *slog.Logger) (func(ctx context.Context) error, error) {
	tlsCfg, err := n.ServerTLS()
	if err != nil {
		return nil, err
	}
	srv := &httpx.Server{
		Name: "cluster", Addr: addr, Handler: n.Handler(version, log), TLS: tlsCfg, Log: log,
		// Cổng có thể mở ra Internet: giới hạn kết nối như cổng agent (cụm chỉ vài chục node).
		MaxConns: 1024, MaxConnsPerIP: 32, IdleTimeout: 60 * time.Second, DisableHTTP2: true,
	}
	return srv.Run, nil
}

// Get gọi một đường dẫn trên node khác qua 5222, đọc JSON vào out.
func (n *Node) Get(ctx context.Context, base, path string, check PeerCheck, out any) error {
	if err := n.do(ctx, http.MethodGet, base, path, check, nil, out); err != nil {
		return fmt.Errorf("%s%s: %w", base, path, err)
	}
	return nil
}

// joinReq: xin vào cụm / chuyển tiếp yêu cầu / xin chữ ký node mới.
type joinReq struct {
	Member cc.Member `json:"member"`
}

type joinSignResp struct {
	Sig string `json:"sig"`
}

// joinRoutes: các đường dẫn thêm node.
func (n *Node) joinRoutes(mux *http.ServeMux, log *slog.Logger) {
	// Xin vào cụm: node chưa thuộc cụm gọi được (đã chứng minh giữ khóa qua TLS). Số yêu cầu chờ
	// bị giới hạn (MaxJoins) và phải được admin duyệt trên mọi node.
	mux.HandleFunc("POST /cluster/v1/join", func(w http.ResponseWriter, r *http.Request) {
		var req joinReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "gói tin không đọc được")
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			httpx.WriteError(w, http.StatusForbidden, "thiếu chứng chỉ")
			return
		}
		pub, _ := r.TLS.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
		if err := n.HandleJoin(req.Member, pub, ""); err != nil {
			httpx.WriteError(w, http.StatusConflict, err.Error())
			return
		}
		log.Warn("có node xin vào cụm, chờ admin duyệt trên mọi node", "node", req.Member.ID, "ten", req.Member.Name,
			"ma_khoa", KeyFingerprint(pub))
		// Chuyển yêu cầu cho các node khác để admin duyệt được trên từng node.
		go func(m cc.Member) {
			if n.c.transport == nil {
				return
			}
			for _, p := range n.peers() {
				ctx, cancel := context.WithTimeout(context.Background(), PeerTimeout)
				_ = n.c.transport.ForwardJoin(ctx, p, m)
				cancel()
			}
		}(req.Member)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.Handle("POST /cluster/v1/join-forward", n.membersOnly(func(w http.ResponseWriter, r *http.Request) {
		var req joinReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "gói tin không đọc được")
			return
		}
		via, _, _ := n.Peer(r)
		if err := n.HandleJoin(req.Member, nil, via); err != nil {
			httpx.WriteError(w, http.StatusConflict, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	// Node đề nghị xin chữ ký của node mới: chỉ node đang xin vào cụm mới trả lời.
	mux.HandleFunc("POST /cluster/v1/join-sign", func(w http.ResponseWriter, r *http.Request) {
		var req commitReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "gói tin không đọc được")
			return
		}
		sig, err := n.HandleJoinSign(req.Decision)
		if err != nil {
			httpx.WriteError(w, http.StatusConflict, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, joinSignResp{Sig: sig})
	})
}

// commitReq: phát một quyết định đã đủ chữ ký.
type commitReq struct {
	Decision *cc.Decision `json:"decision"`
}

type logResp struct {
	Decisions []*cc.Decision `json:"decisions"`
}

// consensusRoutes: các đường dẫn đồng thuận, chỉ node hợp lệ gọi được.
func (n *Node) consensusRoutes(mux *http.ServeMux) {
	mux.Handle("POST /cluster/v1/propose", n.membersOnly(func(w http.ResponseWriter, r *http.Request) {
		var req ProposeReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "gói tin không đọc được")
			return
		}
		from, _, _ := n.Peer(r)
		httpx.WriteJSON(w, http.StatusOK, n.HandlePropose(from, req))
	}))
	mux.Handle("POST /cluster/v1/commit", n.membersOnly(func(w http.ResponseWriter, r *http.Request) {
		var req commitReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "gói tin không đọc được")
			return
		}
		if err := n.HandleCommit(req.Decision); err != nil {
			httpx.WriteError(w, http.StatusConflict, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "seq": n.State().Seq})
	}))
	mux.Handle("GET /cluster/v1/log", n.membersOnly(func(w http.ResponseWriter, r *http.Request) {
		from, err := strconv.ParseUint(r.URL.Query().Get("from"), 10, 64)
		if err != nil || from == 0 {
			httpx.WriteError(w, http.StatusBadRequest, "thiếu from")
			return
		}
		ds, err := n.LogFrom(from)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "không đọc được nhật ký")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, logResp{Decisions: ds})
	}))
}

// HTTPTransport: gọi node khác qua cổng 5222, thử lần lượt các địa chỉ của node đó. Mỗi lần gọi
// ghim đúng khóa của node đích (lấy từ danh sách cụm).
type HTTPTransport struct{ Node *Node }

func (t HTTPTransport) call(ctx context.Context, peer cc.Member, method, path string, in, out any) error {
	pub, err := cc.DecodePub(peer.PubKey)
	if err != nil {
		return err
	}
	var last error = errors.New("node không có địa chỉ cổng cụm")
	for i, base := range peer.Addrs.Cluster {
		// Chia thời gian còn lại cho các địa chỉ chưa thử, để một địa chỉ chết (IP cũ, đường
		// truyền đứt) không ăn hết thời gian của các địa chỉ sau.
		actx, cancel := ctx, context.CancelFunc(func() {})
		if dl, ok := ctx.Deadline(); ok {
			left := time.Until(dl) / time.Duration(len(peer.Addrs.Cluster)-i)
			actx, cancel = context.WithTimeout(ctx, left)
		}
		last = t.Node.do(actx, method, base, path, Pin(pub), in, out)
		cancel()
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return last
		}
		var he *httpError
		if errors.As(last, &he) { // node đã trả lời (từ chối): không thử địa chỉ khác
			return last
		}
	}
	return last
}

func (t HTTPTransport) Propose(ctx context.Context, peer cc.Member, req ProposeReq) (ProposeResp, error) {
	var resp ProposeResp
	err := t.call(ctx, peer, http.MethodPost, "/cluster/v1/propose", req, &resp)
	return resp, err
}

func (t HTTPTransport) Commit(ctx context.Context, peer cc.Member, d *cc.Decision) error {
	return t.call(ctx, peer, http.MethodPost, "/cluster/v1/commit", commitReq{Decision: d}, nil)
}

func (t HTTPTransport) Log(ctx context.Context, peer cc.Member, from uint64) ([]*cc.Decision, error) {
	var resp logResp
	err := t.call(ctx, peer, http.MethodGet, "/cluster/v1/log?from="+strconv.FormatUint(from, 10), nil, &resp)
	return resp.Decisions, err
}

func (t HTTPTransport) JoinSign(ctx context.Context, m cc.Member, d *cc.Decision) (string, error) {
	var resp joinSignResp
	err := t.call(ctx, m, http.MethodPost, "/cluster/v1/join-sign", commitReq{Decision: d}, &resp)
	return resp.Sig, err
}

func (t HTTPTransport) ForwardJoin(ctx context.Context, peer cc.Member, m cc.Member) error {
	return t.call(ctx, peer, http.MethodPost, "/cluster/v1/join-forward", joinReq{Member: m}, nil)
}

// httpError: node đích đã trả lời bằng mã lỗi HTTP.
type httpError struct {
	Status int
	Msg    string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d %s", e.Status, e.Msg) }

// do gọi một đường dẫn trên node khác; in khác nil thì gửi JSON, out khác nil thì đọc JSON.
func (n *Node) do(ctx context.Context, method, base, path string, check PeerCheck, in, out any) error {
	tlsCfg, err := n.ClientTLS(check)
	if err != nil {
		return err
	}
	c := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg, DisableKeepAlives: true}}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &httpError{resp.StatusCode, e.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

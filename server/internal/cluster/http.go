package cluster

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
		httpx.WriteJSON(w, http.StatusOK, Status{NodeID: n.ID, State: n.State(), Time: time.Now().UTC()})
	}))
	return httpx.Chain(mux, httpx.SecureHeaders, httpx.Recover(log), httpx.MaxBody(1<<20),
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
	tlsCfg, err := n.ClientTLS(check)
	if err != nil {
		return err
	}
	c := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg, DisableKeepAlives: true, ForceAttemptHTTP2: false},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return fmt.Errorf("%s%s: HTTP %d %s", base, path, resp.StatusCode, e.Error)
	}
	return json.Unmarshal(body, out)
}

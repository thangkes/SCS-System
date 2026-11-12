package main

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	cc "github.com/thangkes/SCS-System/common/cluster"
	"github.com/thangkes/SCS-System/common/protocol"
	"github.com/thangkes/SCS-System/server/internal/cluster"
	"github.com/thangkes/SCS-System/server/internal/config"
)

const clusterUsage = `scs-server cluster - cụm nhiều SCS-Server (xem docs/thiet-ke-cum-server.md)

  cluster init  -cluster TEN_CUM -name TEN_NODE -addr https://<IP>:5222 [-addr ...] [-agent-addr https://<IP>:6222 ...]
                tạo cụm mới, node này là node đầu tiên (chỉ làm một lần, trên một server)
  cluster status                       xem cụm của node này (danh sách node, mã cụm)
  cluster ping  https://<IP>:5222      gọi cổng cụm của một node khác, in mã node và mã khóa để đối chiếu

  Mọi lệnh nhận -env-file FILE (mặc định ./.env) và -dir THU_MUC (mặc định SCS_CLUSTER_DIR hoặc data/cluster).
  Trên server đã cài: sudo -u scs /opt/scs-server/scs-server cluster ... -env-file /etc/scs-server/server.env
`

// multi: cờ lặp lại được (-addr a -addr b).
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func runCluster(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stderr, clusterUsage)
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("cluster "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envFile := fs.String("env-file", "", "file .env cần nạp")
	dir := fs.String("dir", "", "thư mục cụm (khóa node + nhật ký)")
	name := fs.String("name", "", "init: tên node, ví dụ hn-01")
	clusterName := fs.String("cluster", "", "init: tên cụm, ví dụ scs")
	var addrs, agentAddrs multi
	fs.Var(&addrs, "addr", "init: địa chỉ cổng cụm của node này cho node khác gọi (lặp lại được)")
	fs.Var(&agentAddrs, "agent-addr", "init: địa chỉ cổng agent của node này (lặp lại được)")
	rest := args[1:]
	var target string
	if sub == "ping" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		target, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, clusterUsage)
		return 2
	}
	if _, _, err := loadEnv(*envFile); err != nil {
		fmt.Fprintln(stderr, "lỗi đọc .env:", err)
		return 1
	}
	if *dir == "" {
		*dir = os.Getenv("SCS_CLUSTER_DIR")
	}
	if *dir == "" {
		*dir = "data/cluster"
	}
	n, err := cluster.Open(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "✖", err)
		return 1
	}
	defer n.Close()
	pub := n.Key.Public().(ed25519.PublicKey)

	switch sub {
	case "init":
		if *name == "" || *clusterName == "" || len(addrs) == 0 {
			fmt.Fprint(stderr, clusterUsage)
			return 2
		}
		a := cc.Addrs{Cluster: protocol.CleanServers(addrs), Agent: protocol.CleanServers(agentAddrs)}
		if len(a.Cluster) != len(addrs) || len(a.Agent) != len(agentAddrs) {
			fmt.Fprintln(stderr, "✖ địa chỉ phải có dạng https://<IP hoặc tên miền>:<cổng>, không trùng nhau")
			return 1
		}
		if err := n.Init(*clusterName, *name, a, time.Now()); err != nil {
			fmt.Fprintln(stderr, "✖", err)
			return 1
		}
		st := n.State()
		fmt.Fprintf(stdout, "✔ Đã tạo cụm %q\n", st.Cluster)
		fmt.Fprintf(stdout, "  Mã cụm   : %s\n", st.ClusterID)
		fmt.Fprintf(stdout, "  Node     : %s (%s)\n", *name, n.ID)
		fmt.Fprintf(stdout, "  Mã khóa  : %s\n", cluster.KeyFingerprint(pub))
		fmt.Fprintf(stdout, "  Thư mục  : %s (sao lưu khóa node.key ra nơi an toàn)\n", *dir)
		fmt.Fprintln(stdout, "  Khởi động lại scs-server để mở cổng cụm.")
		return 0
	case "status":
		fmt.Fprintf(stdout, "Node     : %s\nMã khóa  : %s\n", n.ID, cluster.KeyFingerprint(pub))
		if !n.Initialized() {
			fmt.Fprintln(stdout, "Cụm      : chưa thuộc cụm nào (scs-server cluster init ...)")
			return 0
		}
		st := n.State()
		fmt.Fprintf(stdout, "Cụm      : %s\nMã cụm   : %s\nQuyết định: %d (danh sách cụm đời %d)\n",
			st.Cluster, st.ClusterID, st.Seq, st.Epoch)
		valid := len(st.Valid())
		fmt.Fprintf(stdout, "Node hợp lệ: %d, quá nửa = %d\n\n", valid, cc.Majority(valid))
		for _, m := range st.Members {
			me := ""
			if m.ID == n.ID {
				me = "  ← node này"
			}
			fmt.Fprintf(stdout, "  %-12s %s  %-8s %s%s\n", m.Name, m.ID, m.Status, strings.Join(m.Addrs.Cluster, " "), me)
		}
		return 0
	case "ping":
		if target == "" || protocol.CheckServerURL(target) != nil {
			fmt.Fprint(stderr, clusterUsage)
			return 2
		}
		var seen ed25519.PublicKey
		var h cluster.Hello
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := n.Get(ctx, target, "/cluster/v1/hello", cluster.Observe(&seen), &h); err != nil {
			fmt.Fprintln(stderr, "✖ không gọi được:", err)
			return 1
		}
		if cc.NodeID(seen) != h.NodeID {
			fmt.Fprintln(stderr, "✖ mã node trả về không khớp khóa trong chứng chỉ (có thể bị giả mạo)")
			return 1
		}
		fmt.Fprintf(stdout, "Node     : %s\nMã khóa  : %s\nCụm      : %s\n", h.NodeID, cluster.KeyFingerprint(seen), orNone(h.ClusterID))
		if h.Version != "" {
			fmt.Fprintf(stdout, "Phiên bản: %s\n", h.Version)
		}
		if m, ok := n.Trusted(seen); ok {
			fmt.Fprintf(stdout, "✔ Node %s có trong danh sách cụm của node này\n", m.Name)
		} else {
			fmt.Fprintln(stdout, "! Node này KHÔNG có trong danh sách cụm của node đang chạy lệnh")
		}
		fmt.Fprintln(stdout, "Đối chiếu mã khóa với kết quả `cluster status` chạy trên chính node đó.")
		return 0
	}
	fmt.Fprintf(stderr, "lệnh cluster không hợp lệ: %q\n\n%s", sub, clusterUsage)
	return 2
}

func orNone(s string) string {
	if s == "" {
		return "chưa thuộc cụm nào"
	}
	return s
}

// clusterServer: cổng cụm (5222), chỉ chạy khi node đã thuộc một cụm.
func clusterServer(cfg *config.Config, log *slog.Logger) (runner, error) {
	n, err := cluster.Open(cfg.ClusterDir)
	if err != nil {
		return nil, fmt.Errorf("cụm: %w", err)
	}
	if !n.Initialized() {
		log.Info("node chưa thuộc cụm nào, không mở cổng cụm", "node", n.ID, "thu_muc", cfg.ClusterDir)
		n.Close()
		return nil, nil
	}
	st := n.State()
	log.Info("cụm", "ten", st.Cluster, "ma_cum", cluster.Short(st.ClusterID), "node", n.ID,
		"so_node_hop_le", len(st.Valid()), "cong", cfg.ClusterListen)
	run, err := n.Server(cfg.ClusterListen, version, log)
	if err != nil {
		n.Close()
		return nil, err
	}
	n.SetTransport(cluster.HTTPTransport{Node: n})
	return func(ctx context.Context) error {
		defer n.Close()
		// Định kỳ bắt kịp nhật ký từ node khác (node vừa bật lại, hoặc lỡ một lần phát).
		go n.Run(ctx, 30*time.Second, func(err error) { log.Warn("chưa bắt kịp nhật ký cụm", "err", err) })
		return run(ctx)
	}, nil
}

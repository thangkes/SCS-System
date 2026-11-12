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
  cluster join  https://<IP>:5222 -fingerprint MA_KHOA -name TEN_NODE -addr https://<IP node này>:5222 [-agent-addr ...]
                xin vào một cụm đã có qua một node của cụm đó. MA_KHOA là "Mã khóa" in ra khi chạy
                cluster status trên node kia (chống kẻ đứng giữa). Sau đó khởi động lại scs-server và
                nhờ admin duyệt trên web của MỌI node trong cụm (trang Cụm server).

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
	fingerprint := fs.String("fingerprint", "", "join: mã khóa của node đích (cluster status trên node đó)")
	var addrs, agentAddrs multi
	fs.Var(&addrs, "addr", "init: địa chỉ cổng cụm của node này cho node khác gọi (lặp lại được)")
	fs.Var(&agentAddrs, "agent-addr", "init: địa chỉ cổng agent của node này (lặp lại được)")
	rest := args[1:]
	var target string
	if (sub == "ping" || sub == "join") && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
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
			if j := n.Local().Joining; j != nil {
				fmt.Fprintf(stdout, "Cụm      : ĐANG XIN VÀO CỤM qua %s (từ %s), tên node %s\n", j.Target,
					j.Since.Local().Format("02/01 15:04"), j.Member.Name)
				fmt.Fprintln(stdout, "           chờ admin duyệt trên web của mọi node trong cụm; scs-server phải đang chạy")
				return 0
			}
			fmt.Fprintln(stdout, "Cụm      : chưa thuộc cụm nào (scs-server cluster init ... hoặc cluster join ...)")
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
	case "join":
		if target == "" || protocol.CheckServerURL(target) != nil || *name == "" || *fingerprint == "" || len(addrs) == 0 {
			fmt.Fprint(stderr, clusterUsage)
			return 2
		}
		a := cc.Addrs{Cluster: protocol.CleanServers(addrs), Agent: protocol.CleanServers(agentAddrs)}
		if len(a.Cluster) != len(addrs) || len(a.Agent) != len(agentAddrs) {
			fmt.Fprintln(stderr, "✖ địa chỉ phải có dạng https://<IP hoặc tên miền>:<cổng>, không trùng nhau")
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Bước 1: xem khóa node đích rồi so với mã khóa admin đưa. Sai là dừng, chưa gửi gì.
		var seen ed25519.PublicKey
		var h cluster.Hello
		if err := n.Get(ctx, target, "/cluster/v1/hello", cluster.Observe(&seen), &h); err != nil {
			fmt.Fprintln(stderr, "✖ không gọi được node đích:", err)
			return 1
		}
		if got := cluster.KeyFingerprint(seen); !strings.EqualFold(got, strings.TrimSpace(*fingerprint)) {
			fmt.Fprintf(stderr, "✖ mã khóa node đích là %s, KHÁC mã bạn đưa (%s).\n  Có thể sai địa chỉ hoặc có kẻ đứng giữa. Không gửi yêu cầu.\n", got, *fingerprint)
			return 1
		}
		// Bước 2: gửi yêu cầu, ghim đúng khóa vừa đối chiếu.
		if err := n.StartJoin(ctx, target, seen, *name, a); err != nil {
			fmt.Fprintln(stderr, "✖", err)
			return 1
		}
		fmt.Fprintf(stdout, "✔ Đã gửi yêu cầu xin vào cụm %s qua node %s\n", orNone(h.ClusterID), h.NodeID)
		fmt.Fprintf(stdout, "  Node này : %s (%s)\n", *name, n.ID)
		fmt.Fprintf(stdout, "  Mã khóa  : %s   ← admin đối chiếu mã này khi duyệt\n", cluster.KeyFingerprint(pub))
		fmt.Fprintln(stdout, "  Tiếp theo:")
		fmt.Fprintln(stdout, "   1. Khởi động lại scs-server trên node này (để mở cổng cụm và tự nhận kết quả).")
		fmt.Fprintln(stdout, "   2. Admin đăng nhập web của TỪNG node trong cụm → trang Cụm server → so mã khóa → Duyệt.")
		fmt.Fprintln(stdout, "   3. Đủ mọi node duyệt thì node này tự vào cụm (xem: scs-server cluster status).")
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

// clusterServer: cổng cụm (5222). Chạy khi node đã thuộc một cụm hoặc đang xin vào cụm.
func clusterServer(cfg *config.Config, log *slog.Logger, n *cluster.Node) (runner, error) {
	switch {
	case n.Initialized():
		st := n.State()
		log.Info("cụm", "ten", st.Cluster, "ma_cum", cluster.Short(st.ClusterID), "node", n.ID,
			"so_node_hop_le", len(st.Valid()), "cong", cfg.ClusterListen)
	case n.Joining():
		log.Info("node đang xin vào cụm, chờ admin duyệt trên mọi node của cụm", "node", n.ID,
			"ma_khoa", cluster.KeyFingerprint(n.Key.Public().(ed25519.PublicKey)), "cong", cfg.ClusterListen)
	default:
		log.Info("node chưa thuộc cụm nào, không mở cổng cụm", "node", n.ID, "thu_muc", cfg.ClusterDir)
		return nil, nil
	}
	run, err := n.Server(cfg.ClusterListen, version, log)
	if err != nil {
		return nil, err
	}
	n.SetTransport(cluster.HTTPTransport{Node: n})
	return func(ctx context.Context) error {
		// Định kỳ: node đang xin vào cụm thì thử lấy nhật ký; node trong cụm thì bắt kịp nhật ký và
		// thử lại các việc admin đã xác nhận mà chưa đủ node.
		go n.Run(ctx, 30*time.Second,
			func(err error) { log.Warn("cụm: việc định kỳ chưa xong", "err", err) },
			func() {
				st := n.State()
				log.Info("node đã được thêm vào cụm", "ten", st.Cluster, "ma_cum", cluster.Short(st.ClusterID), "so_node_hop_le", len(st.Valid()))
			})
		return run(ctx)
	}, nil
}

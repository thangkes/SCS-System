package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"

	"github.com/thangkes/SCS-System/server/internal/pki"
)

// listFlag: cờ lặp lại được (-ip a -ip b) hoặc ngăn bằng dấu phẩy (-ip a,b).
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}

// runPKI: scs-server pki init | server  -ip <IP> [-ip ...] [-dns <tên>] [-dir data/pki]
//
//	init    tạo CA mới + chứng chỉ server. Không ghi đè file đã có.
//	server  cấp chứng chỉ server mới từ CA đã có (đổi IP WAN). Xóa server.pem/server.key cũ trước.
func runPKI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "init" && args[0] != "server") {
		fmt.Fprintln(stderr, "cách dùng: scs-server pki init|server -ip <IP WAN> [-ip <IP VPN>] [-dns <tên>] [-dir data/pki]")
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("pki "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var ipArgs, dns listFlag
	fs.Var(&ipArgs, "ip", "IP ghi vào chứng chỉ server: IP WAN agent gọi tới, IP VPN vào web (lặp lại được)")
	fs.Var(&dns, "dns", "tên miền ghi vào chứng chỉ server (tùy chọn)")
	dir := fs.String("dir", "data/pki", "thư mục chứa CA và chứng chỉ")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	var ips []net.IP
	for _, s := range ipArgs {
		ip := net.ParseIP(s)
		if ip == nil {
			fmt.Fprintf(stderr, "-ip %q không phải địa chỉ IP\n", s)
			return 2
		}
		ips = append(ips, ip)
	}
	if len(ips) == 0 && len(dns) == 0 {
		fmt.Fprintln(stderr, "cần ít nhất một -ip (IP WAN của server mà agent gọi tới)")
		return 2
	}

	caCert, caKey := filepath.Join(*dir, "ca.pem"), filepath.Join(*dir, "ca.key")
	srvCert, srvKey := filepath.Join(*dir, "server.pem"), filepath.Join(*dir, "server.key")
	fail := func(err error) int {
		fmt.Fprintln(stderr, "lỗi:", err)
		return 1
	}

	var ca *pki.CA
	if sub == "init" {
		var keyPEM []byte
		var err error
		if ca, keyPEM, err = pki.NewCA("SCS-System CA"); err != nil {
			return fail(err)
		}
		if err := pki.WriteFile(caCert, ca.CertPEM, false); err != nil {
			return fail(err)
		}
		if err := pki.WriteFile(caKey, keyPEM, true); err != nil {
			return fail(err)
		}
	} else {
		var err error
		if ca, err = pki.Load(caCert, caKey); err != nil {
			return fail(err)
		}
	}
	cert, key, err := ca.IssueServer(ips, dns)
	if err != nil {
		return fail(err)
	}
	if err := pki.WriteFile(srvCert, cert, false); err != nil {
		return fail(err)
	}
	if err := pki.WriteFile(srvKey, key, true); err != nil {
		return fail(err)
	}

	abs := func(p string) string {
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return p
	}
	fmt.Fprintf(stdout, `Đã tạo trong %s:
  ca.pem       CA nội bộ (chép cho agent thành /etc/scs-agent/server-ca.pem)
  ca.key       khóa CA, quyền 600: GIỮ KÍN, ai có file này cấp được chứng chỉ giả cho máy
  server.pem   chứng chỉ server, hợp lệ cho: %s
  server.key   khóa server, quyền 600

Thêm vào .env (hoặc /etc/scs-server/server.env) rồi khởi động lại server:
  SCS_TLS_CERT=%s
  SCS_TLS_KEY=%s
  SCS_TLS_CLIENT_CA=%s
  SCS_CA_CERT=%s
  SCS_CA_KEY=%s
`, abs(*dir), strings.Join(append(append([]string{}, ipArgs...), dns...), ", "),
		abs(srvCert), abs(srvKey), abs(caCert), abs(caCert), abs(caKey))
	return 0
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/thangkes/SCS-System/server/internal/addrbook"
)

const serversUsage = `scs-server servers - danh sách địa chỉ server gửi cho agent (IP LAN, IP WAN, tên miền DDNS, IP VPN)

  servers list                              in danh sách (thứ tự = thứ tự agent thử)
  servers add URL [-note GHI_CHU] [-first]  thêm địa chỉ, ví dụ https://ten.ddns.net:16222
  servers remove URL                        gỡ địa chỉ (không gỡ được địa chỉ cuối cùng)
  servers set URL[,URL...]                  thay cả danh sách (ghi đè được file hỏng)

  Server đang chạy tự đọc lại file trong vài giây, không phải khởi động lại. Agent nhận danh sách ở
  heartbeat kế tiếp (≤ 5 phút). Số máy đã nhận / đang dùng từng địa chỉ xem trên web, trang "Địa chỉ server".
  Đổi mạng: thêm địa chỉ mới TRƯỚC, chờ agent nhận, chuyển mạng, rồi mới gỡ địa chỉ cũ.

  Mọi lệnh nhận -env-file FILE (mặc định ./.env) và -file FILE (mặc định SCS_AGENT_SERVERS_FILE).
  Trên server đã cài: sudo -u scs /opt/scs-server/scs-server servers list -env-file /etc/scs-server/server.env
`

func runServers(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stderr, serversUsage)
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("servers "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envFile := fs.String("env-file", "", "file .env cần nạp")
	file := fs.String("file", "", "file danh sách (mặc định SCS_AGENT_SERVERS_FILE)")
	note := fs.String("note", "", "add: ghi chú, ví dụ \"Địa điểm mới - VNPT\"")
	first := fs.Bool("first", false, "add: đặt lên đầu danh sách")
	rest := args[1:]
	var target string
	if (sub == "add" || sub == "remove" || sub == "set") && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		target, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "thừa tham số: %v\n", fs.Args())
		return 2
	}
	if _, _, err := loadEnv(*envFile); err != nil {
		fmt.Fprintln(stderr, "lỗi đọc .env:", err)
		return 1
	}
	path := *file
	if path == "" {
		path = os.Getenv("SCS_AGENT_SERVERS_FILE")
	}
	if path == "" {
		path = "data/agent-servers.json"
	}
	b, openErr := addrbook.Open(path, strings.Split(os.Getenv("SCS_AGENT_SERVERS"), ","))
	if openErr != nil && sub != "set" {
		fmt.Fprintf(stderr, "file danh sách bị lỗi: %v\nghi lại cả danh sách bằng: scs-server servers set URL[,URL...]\n", openErr)
		return 1
	}
	need := func() bool {
		if target == "" {
			fmt.Fprintf(stderr, "thiếu địa chỉ: scs-server servers %s URL\n", sub)
			return false
		}
		return true
	}
	var err error
	switch sub {
	case "list":
	case "add":
		if !need() {
			return 2
		}
		_, err = b.Add(target, *note, *first)
	case "remove":
		if !need() {
			return 2
		}
		err = b.Remove(target)
	case "set":
		if !need() {
			return 2
		}
		err = b.Set(strings.Split(target, ","))
	default:
		fmt.Fprintf(stderr, "lệnh không hợp lệ: servers %q\n\n%s", sub, serversUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "không làm được:", err)
		if errors.Is(err, addrbook.ErrLast) {
			fmt.Fprintln(stderr, "thêm địa chỉ mới trước rồi mới gỡ địa chỉ này")
		}
		return 1
	}
	es := b.Entries()
	fmt.Fprintf(stdout, "File: %s\n", b.Path())
	if len(es) == 0 {
		fmt.Fprintln(stdout, "Chưa có địa chỉ nào: agent chỉ dùng địa chỉ lúc cài.")
		return 0
	}
	for i, e := range es {
		line := fmt.Sprintf("  %d. %s", i+1, e.URL)
		if e.Note != "" {
			line += "   (" + e.Note + ")"
		}
		fmt.Fprintln(stdout, line)
	}
	if sub != "list" {
		fmt.Fprintln(stdout, "Đã lưu. Agent nhận ở heartbeat kế tiếp (≤ 5 phút); xem số máy đã nhận trên web, trang \"Địa chỉ server\".")
	}
	return 0
}

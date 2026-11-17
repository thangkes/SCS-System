package main

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/thangkes/SCS-System/server/internal/cluster"
	"github.com/thangkes/SCS-System/server/internal/pki"
	"github.com/thangkes/SCS-System/server/internal/rulefile"
	"github.com/thangkes/SCS-System/server/internal/rulestore"
)

const ruleStoreUsage = `
  Kho bộ rule trên server (SCS_RULES_DIR; web: trang "Rule quét log"; menu mục 16):
  rules list                            liệt kê, tự chia: chưa ký / đã ký / cần ký lại / lỗi
  rules add FILE.yaml|-                 kiểm, chạy tests rồi đưa vào kho ("-" = đọc từ stdin)
  rules approve FILE                    duyệt và ký một bộ trong kho (FILE = tên file ở cột "File")
  rules remove FILE [-bundle]           xóa file nguồn (-bundle: xóa luôn bản ký)
  rules export TÊN@PHIÊN_BẢN [-o F]     in / ghi bộ rule đã ký để chép sang agent
    Các lệnh kho nhận: -env-file FILE (mặc định ./.env), -dir THƯ_MỤC (mặc định SCS_RULES_DIR),
    -key KHÓA (mặc định SCS_RELEASE_KEY), -cluster-dir (mặc định SCS_CLUSTER_DIR).
    Server trong cụm: duyệt bộ mới phải làm trên web (gửi đề nghị cho các server khác ký);
    lệnh approve chỉ ký được bộ cụm đã duyệt đúng nội dung đó.
    Server đã cài: sudo -u scs /opt/scs-server/scs-server rules list -env-file /etc/scs-server/server.env
`

func ruleStoreCmd(sub string) bool {
	switch sub {
	case "list", "add", "approve", "remove", "export":
		return true
	}
	return false
}

var statusLabel = map[string]string{
	rulestore.StatusUnsigned: "CHƯA KÝ",
	rulestore.StatusSigned:   "ĐÃ KÝ",
	rulestore.StatusStale:    "CẦN KÝ LẠI",
	rulestore.StatusInvalid:  "LỖI",
}

func runRuleStore(sub string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envFile := fs.String("env-file", "", "file .env cần nạp")
	dir := fs.String("dir", "", "thư mục kho (mặc định SCS_RULES_DIR)")
	keyPath := fs.String("key", "", "khóa phát hành (mặc định SCS_RELEASE_KEY)")
	clusterDir := fs.String("cluster-dir", "", "thư mục cụm (mặc định SCS_CLUSTER_DIR)")
	withBundle := fs.Bool("bundle", false, "remove: xóa luôn bản ký")
	out := fs.String("o", "", "export: ghi ra file thay vì in")
	var target string
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") || len(rest) > 0 && rest[0] == "-" {
		target, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, rulesUsage)
		return 2
	}
	if _, _, err := loadEnv(*envFile); err != nil {
		fmt.Fprintln(stderr, "lỗi đọc .env:", err)
		return 1
	}
	pick := func(flagV, env, def string) string {
		if flagV != "" {
			return flagV
		}
		if v := os.Getenv(env); v != "" {
			return v
		}
		return def
	}
	*dir = pick(*dir, "SCS_RULES_DIR", "data/rules")
	*keyPath = pick(*keyPath, "SCS_RELEASE_KEY", "")
	*clusterDir = pick(*clusterDir, "SCS_CLUSTER_DIR", "data/cluster")

	var priv ed25519.PrivateKey
	var pub ed25519.PublicKey
	if *keyPath != "" {
		k, err := pki.LoadReleaseKey(*keyPath)
		if err != nil && sub == "approve" {
			fmt.Fprintln(stderr, "✖", err)
			return 1
		}
		if err == nil {
			priv, pub = k, k.Public().(ed25519.PublicKey)
		}
	}
	s, err := rulestore.Open(*dir, pub)
	if err != nil {
		fmt.Fprintln(stderr, "✖ kho bộ rule:", err)
		return 1
	}
	log, _ := cluster.ReadLog(*clusterDir)
	ap := rulestore.Approvals(log)
	inCluster := len(log) > 0
	need := func(what string) bool {
		if target == "" {
			fmt.Fprintf(stderr, "thiếu %s: scs-server rules %s %s\n", what, sub, strings.ToUpper(what))
			return false
		}
		return true
	}

	switch sub {
	case "list":
		return printRuleStore(s, ap, inCluster, pub != nil, stdout, stderr)
	case "add":
		if !need("file") {
			return 2
		}
		var data []byte
		if target == "-" {
			data, err = io.ReadAll(io.LimitReader(os.Stdin, rulefile.MaxFile+1))
		} else {
			data, err = os.ReadFile(target)
		}
		if err != nil {
			fmt.Fprintln(stderr, "✖", err)
			return 1
		}
		e, err := s.Add(data, func(name, ver, sha string) error {
			if x, ok := ap[rulestore.Key(name, ver)]; ok && x.SHA256 != sha {
				return fmt.Errorf("%w: cụm đã duyệt %s với nội dung khác (quyết định #%d); đổi version", rulestore.ErrConflict, rulestore.Key(name, ver), x.Seq)
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(stderr, "✖ không thêm được\n%s\n", indent(err.Error()))
			return 1
		}
		fmt.Fprintf(stdout, "✔ Đã thêm %s (file %s): %d rule, %d đang bật, %d dòng test đạt — %s\n",
			rulestore.Key(e.Name, e.Version), e.File, e.Rules, e.Enabled, e.Tests, statusLabel[e.Status])
		return 0
	case "approve":
		if !need("file") {
			return 2
		}
		if priv == nil {
			fmt.Fprintln(stderr, "✖ chưa có khóa phát hành (SCS_RELEASE_KEY / -key): không ký được")
			return 1
		}
		set, sha, err := s.Prepare(target)
		if err != nil {
			fmt.Fprintf(stderr, "✖ %s\n%s\n", target, indent(err.Error()))
			return 1
		}
		var seq uint64
		want := ""
		if inCluster {
			x, ok := ap[rulestore.Key(set.Name, set.Version)]
			switch {
			case !ok:
				fmt.Fprintln(stderr, "✖ server này thuộc một cụm: duyệt bộ rule mới trên web quản trị (trang \"Rule quét log\")\n"+
					"  để gửi đề nghị cho các server khác ký. Lệnh này chỉ ký được bộ cụm đã duyệt.")
				return 1
			case x.SHA256 != sha:
				fmt.Fprintf(stderr, "✖ cụm đã duyệt %s với nội dung khác (quyết định #%d): đổi version\n", rulestore.Key(set.Name, set.Version), x.Seq)
				return 1
			}
			seq, want = x.Seq, sha
		}
		by := "menu"
		if u := os.Getenv("SUDO_USER"); u != "" {
			by = "menu (" + u + ")"
		}
		e, err := s.Sign(target, priv, by, seq, want)
		if err != nil {
			fmt.Fprintf(stderr, "✖ không ký được\n%s\n", indent(err.Error()))
			return 1
		}
		fmt.Fprintf(stdout, "✔ Đã duyệt và ký %s (sha256 %s)\n  Bản ký: %s/signed/%s.json\n",
			rulestore.Key(e.Name, e.Version), e.SHA256[:16], s.Dir, rulestore.Key(e.Name, e.Version))
		return 0
	case "remove":
		if !need("file") {
			return 2
		}
		if err := s.Remove(target, *withBundle); err != nil {
			fmt.Fprintln(stderr, "✖", err)
			return 1
		}
		fmt.Fprintln(stdout, "✔ Đã xóa", target)
		return 0
	case "export":
		if !need("tên@phiên_bản") {
			return 2
		}
		at := strings.LastIndex(target, "@")
		if at <= 0 {
			fmt.Fprintln(stderr, "✖ dạng TÊN@PHIÊN_BẢN, ví dụ cong-ty@2026.11.20-01")
			return 2
		}
		b, err := s.Bundle(target[:at], target[at+1:])
		if err != nil {
			fmt.Fprintln(stderr, "✖", err, "(chưa ký?)")
			return 1
		}
		if *out == "" {
			_, _ = stdout.Write(b)
			return 0
		}
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintln(stderr, "✖", err)
			return 1
		}
		fmt.Fprintln(stdout, "✔ Đã ghi", *out)
		return 0
	}
	return 2
}

func printRuleStore(s *rulestore.Store, ap map[string]rulestore.Approval, inCluster, checkSig bool, stdout, stderr io.Writer) int {
	list, err := s.List()
	if err != nil {
		fmt.Fprintln(stderr, "✖", err)
		return 1
	}
	fmt.Fprintf(stdout, "Kho bộ rule: %s\n", s.Dir)
	if !checkSig {
		fmt.Fprintln(stdout, "  (không đọc được khóa phát hành: chưa kiểm chữ ký của bản đã ký)")
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "  Chưa có bộ rule nào. Thêm bằng: rules add FILE.yaml")
	}
	order := []string{rulestore.StatusUnsigned, rulestore.StatusStale, rulestore.StatusInvalid, rulestore.StatusSigned}
	have := map[string]bool{}
	for _, st := range order {
		var group []rulestore.Entry
		for _, e := range list {
			if e.Status == st {
				group = append(group, e)
			}
		}
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(stdout, "\n%s (%d)\n", statusLabel[st], len(group))
		for _, e := range group {
			k := rulestore.Key(e.Name, e.Version)
			have[k] = true
			label := k
			if e.Name == "" {
				label = "(không đọc được)"
			}
			line := fmt.Sprintf("  %-40s", label)
			if e.File != "" {
				line += "  File: " + e.File
			} else {
				line += "  (chỉ còn bản ký, không có file nguồn)"
			}
			if e.Rules > 0 {
				line += fmt.Sprintf("  · %d rule (%d bật), %d test", e.Rules, e.Enabled, e.Tests)
			}
			if e.Signed != nil {
				line += fmt.Sprintf("  · ký %s bởi %s", e.Signed.At.Local().Format("02/01/2006 15:04"), e.Signed.By)
			}
			if x, ok := ap[k]; ok {
				line += fmt.Sprintf("  · cụm duyệt #%d", x.Seq)
			} else if inCluster && e.Name != "" {
				line += "  · cụm chưa duyệt"
			}
			fmt.Fprintln(stdout, line)
			if e.Error != "" {
				fmt.Fprintln(stdout, indent(indent(firstLines(e.Error, 6))))
			}
		}
	}
	var only []string
	for k, x := range ap {
		if !have[k] {
			only = append(only, fmt.Sprintf("  %s  (quyết định #%d) — thêm đúng file đó vào kho rồi duyệt là ký được", k, x.Seq))
		}
	}
	if len(only) > 0 {
		fmt.Fprintf(stdout, "\nCỤM ĐÃ DUYỆT, SERVER NÀY CHƯA CÓ FILE (%d)\n%s\n", len(only), strings.Join(only, "\n"))
	}
	return 0
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("… (%d dòng nữa)", len(lines)-n))
	}
	return strings.Join(lines, "\n")
}

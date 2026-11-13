package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/thangkes/SCS-System/common/rules"
	"github.com/thangkes/SCS-System/server/internal/pki"
	"github.com/thangkes/SCS-System/server/internal/rulefile"
)

const rulesUsage = `scs-server rules - viết và kiểm rule quét log (xem docs/huong-dan-viet-rule.md)

  rules check FILE.yaml...              kiểm cú pháp, giới hạn và chạy phần tests của từng rule
  rules test  FILE.yaml -log LOG        chạy bộ rule trên một file log mẫu, in các sự kiện sẽ báo
              [-source NGUON] [-unit UNIT] [-step 1s] [-ctx role=pri,os=ubuntu-22.04]
                -source: dòng trong LOG coi như đến từ nguồn nào (mặc định: nguồn của rule đầu tiên)
                -unit:   với journald, unit của các dòng (log lấy bằng: journalctl -u UNIT -o cat)
                -step:   khoảng cách thời gian giả lập giữa hai dòng (để thử aggregate)
                -ctx:    ngữ cảnh máy giả lập: chỉ chạy rule có applies_to khớp (bỏ trống: mọi rule)
  rules build FILE.yaml [-key data/pki/release.key] [-o FILE.json]
                                        kiểm rồi ký thành bộ rule gửi agent (mặc định in ra stdout)
`

func runRules(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stderr, rulesUsage)
		return 2
	}
	switch args[0] {
	case "check":
		return rulesCheck(args[1:], stdout, stderr)
	case "test":
		return rulesTest(args[1:], stdout, stderr)
	case "build":
		return rulesBuild(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "lệnh rules không hợp lệ: %q\n\n%s", args[0], rulesUsage)
	return 2
}

func rulesCheck(files []string, stdout, stderr io.Writer) int {
	if len(files) == 0 {
		fmt.Fprint(stderr, rulesUsage)
		return 2
	}
	code := 0
	for _, f := range files {
		s, err := rulefile.Load(f)
		if err != nil {
			fmt.Fprintf(stderr, "✖ %s\n%s\n", f, indent(err.Error()))
			code = 1
			continue
		}
		if fails := s.RunTests(); len(fails) > 0 {
			fmt.Fprintf(stderr, "✖ %s: tests không đạt\n%s\n", f, indent(strings.Join(fails, "\n")))
			code = 1
			continue
		}
		on, tests := 0, 0
		for _, r := range s.Rules {
			if !r.Disabled {
				on++
			}
			if r.Tests != nil {
				tests += len(r.Tests.Match) + len(r.Tests.NoMatch)
			}
		}
		fmt.Fprintf(stdout, "✔ %s: bộ %s phiên bản %s, %d rule (%d đang bật), %d dòng test đạt\n",
			f, s.Name, s.Version, len(s.Rules), on, tests)
	}
	return code
}

// splitFile tách FILE đứng đầu khỏi các cờ (cho phép viết: rules test FILE -log x).
func splitFile(fs *flag.FlagSet, args []string) (string, bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", false
	}
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return "", false
	}
	return args[0], true
}

func rulesTest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	logPath := fs.String("log", "", "file log mẫu")
	source := fs.String("source", "", "nguồn của các dòng")
	unit := fs.String("unit", "", "unit journald của các dòng")
	step := fs.Duration("step", time.Second, "khoảng thời gian giả lập giữa hai dòng")
	ctxFlag := fs.String("ctx", "", "ngữ cảnh máy giả lập, ví dụ role=pri,os=ubuntu-22.04")
	file, ok := splitFile(fs, args)
	if !ok || *logPath == "" {
		fmt.Fprint(stderr, rulesUsage)
		return 2
	}
	s, err := rulefile.Load(file)
	if err != nil {
		fmt.Fprintf(stderr, "✖ %s\n%s\n", file, indent(err.Error()))
		return 1
	}
	mctx, err := rules.ParseContext(*ctxFlag)
	if err != nil {
		fmt.Fprintln(stderr, "✖", err)
		return 2
	}
	e, err := rules.CompileFor(s, mctx)
	if err != nil {
		fmt.Fprintln(stderr, "✖", err)
		return 1
	}
	if sk := e.Skipped(); len(sk) > 0 {
		fmt.Fprintf(stdout, "Ngữ cảnh %s: bỏ %d rule không khớp applies_to: %s\n", mctx, len(sk), strings.Join(sk, ", "))
	}
	if *source == "" { // CompileFor bảo đảm có ít nhất một rule chạy
		for _, r := range s.Rules {
			if !r.Disabled && r.Applies(mctx) {
				*source, *unit = r.Source, r.Unit
				break
			}
		}
	}
	if !e.Covers(*source, *unit) {
		fmt.Fprintf(stderr, "! Không có rule nào đọc nguồn %q unit %q: sẽ không có sự kiện nào\n", *source, *unit)
	}
	f, err := os.Open(*logPath)
	if err != nil {
		fmt.Fprintln(stderr, "✖", err)
		return 1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	lines, events := 0, 0
	byRule := map[string]int{}
	enc := json.NewEncoder(stdout)
	for sc.Scan() {
		lines++
		for _, ev := range e.Feed(rules.Line{Source: *source, Unit: *unit, Time: t, Text: sc.Text()}) {
			events++
			byRule[ev.RuleID]++
			fmt.Fprintf(stdout, "dòng %d → %s [%s] count=%d\n  ", lines, ev.RuleID, ev.Severity, ev.Count)
			_ = enc.Encode(ev)
		}
		t = t.Add(*step)
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(stderr, "✖ đọc log:", err)
		return 1
	}
	from := *source
	if *unit != "" {
		from += ", unit " + *unit
	}
	fmt.Fprintf(stdout, "\nĐã đọc %d dòng (nguồn %s), %d sự kiện.\n", lines, from, events)
	for id, n := range byRule {
		fmt.Fprintf(stdout, "  %-24s %d\n", id, n)
	}
	return 0
}

func rulesBuild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyPath := fs.String("key", "data/pki/release.key", "khóa phát hành (tạo bằng: scs-server pki release)")
	out := fs.String("o", "", "file bộ rule đã ký (bỏ trống: in ra stdout)")
	file, ok := splitFile(fs, args)
	if !ok {
		fmt.Fprint(stderr, rulesUsage)
		return 2
	}
	s, err := rulefile.Load(file)
	if err == nil {
		if fails := s.RunTests(); len(fails) > 0 {
			err = fmt.Errorf("tests không đạt:\n%s", strings.Join(fails, "\n"))
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "✖ %s\n%s\n", file, indent(err.Error()))
		return 1
	}
	priv, err := pki.LoadReleaseKey(*keyPath)
	if err != nil {
		fmt.Fprintln(stderr, "✖", err)
		return 1
	}
	b, err := rules.Build(s, priv)
	if err != nil {
		fmt.Fprintln(stderr, "✖", err)
		return 1
	}
	data, _ := json.MarshalIndent(b, "", "  ")
	data = append(data, '\n')
	if *out == "" {
		_, _ = stdout.Write(data)
		return 0
	}
	// Ghi file tạm rồi đổi tên: build lại cùng tên thì thay hẳn, không bao giờ để file dở dang.
	tmp := *out + ".tmp"
	err = os.WriteFile(tmp, data, 0o644)
	if err == nil {
		err = os.Rename(tmp, *out)
	}
	if err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintln(stderr, "✖", err)
		return 1
	}
	fmt.Fprintf(stdout, "✔ Đã ký bộ %s phiên bản %s → %s (sha256 %s)\n", b.Name, b.Version, *out, b.SHA256[:16])
	return 0
}

func indent(s string) string { return "  " + strings.ReplaceAll(s, "\n", "\n  ") }

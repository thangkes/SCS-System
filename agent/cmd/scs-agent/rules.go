package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/thangkes/SCS-System/agent/internal/config"
	"github.com/thangkes/SCS-System/agent/internal/scan"
	"github.com/thangkes/SCS-System/common/rules"
)

const rulesUsage = `scs-agent rules - xem và chạy thử bộ rule trên máy này

  scs-agent rules                          bộ rule đang dùng: phiên bản, các rule, nguồn log cần đọc
  scs-agent rules -install FILE.json       cài tay một bộ rule đã ký (scs-server rules build), cần root
  scs-agent rules -log FILE|- -source NGUON [-unit UNIT]
                                           chạy bộ rule đang dùng trên một file log (- là stdin), in sự kiện
    ví dụ: sudo scs-agent rules -log /var/log/auth.log -source /var/log/auth.log
           journalctl -k -o cat --since today | sudo scs-agent rules -log - -source journald -unit kernel
`

func runRules(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, rulesUsage) }
	cfgPath := fs.String("config", "", "file cấu hình (mặc định "+config.DefaultFile+")")
	install := fs.String("install", "", "file bộ rule đã ký cần cài")
	logPath := fs.String("log", "", "file log chạy thử (- là stdin)")
	source := fs.String("source", "", "nguồn của các dòng: đường dẫn file trong /var/log hoặc journald")
	unit := fs.String("unit", "", "unit journald của các dòng")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "cấu hình không hợp lệ:\n"+err.Error())
		return 1
	}

	var e *rules.Engine
	if *install != "" {
		data, err := os.ReadFile(*install)
		if err == nil {
			e, err = scan.Install(cfg.StateDir, cfg.ReleasePub, data)
		}
		if err != nil {
			fmt.Fprintln(stderr, "✖ không cài được bộ rule (bộ đang dùng giữ nguyên):", err)
			return 1
		}
		fmt.Fprintf(stdout, "✔ Đã cài bộ rule %s phiên bản %s\n", e.Set().Name, e.Set().Version)
	} else if e, err = scan.Load(cfg.StateDir, cfg.ReleasePub); err != nil {
		if errors.Is(err, scan.ErrNoBundle) {
			fmt.Fprintf(stderr, "Máy chưa có bộ rule (%s).\n", scan.BundlePath(cfg.StateDir))
		} else {
			fmt.Fprintln(stderr, "✖ bộ rule đang lưu không dùng được:", err)
		}
		return 1
	}

	if *logPath == "" {
		printRuleSet(stdout, e)
		return 0
	}
	if *source == "" {
		fmt.Fprintln(stderr, "thiếu -source (đường dẫn file log mà các dòng thuộc về, hoặc journald)")
		return 2
	}
	if !e.Covers(*source, *unit) {
		fmt.Fprintf(stderr, "! Không có rule nào đọc nguồn %q unit %q: sẽ không có sự kiện nào (xem nguồn bằng: scs-agent rules)\n", *source, *unit)
	}
	in := io.Reader(os.Stdin)
	if *logPath != "-" {
		f, err := os.Open(*logPath)
		if err != nil {
			fmt.Fprintln(stderr, "✖", err)
			return 1
		}
		defer f.Close()
		in = f
	}
	n := 0
	enc := json.NewEncoder(stdout)
	err = scan.Run(context.Background(), e, scan.ReaderSource{R: in, Source: *source, Unit: *unit}, func(ev rules.Event) {
		n++
		_ = enc.Encode(ev)
	})
	if err != nil {
		fmt.Fprintln(stderr, "✖", err)
		return 1
	}
	fmt.Fprintf(stdout, "%d sự kiện.\n", n)
	return 0
}

func printRuleSet(w io.Writer, e *rules.Engine) {
	s := e.Set()
	fmt.Fprintf(w, "Bộ rule : %s\nPhiên bản: %s\n", s.Name, s.Version)
	if s.Description != "" {
		fmt.Fprintf(w, "Mô tả   : %s\n", s.Description)
	}
	fmt.Fprintln(w, "\nNguồn log cần đọc:")
	for _, sp := range e.Sources() {
		units := ""
		if sp.Source == rules.SourceJournald {
			units = " (toàn bộ journal)"
			if len(sp.Units) > 0 {
				units = " unit: " + strings.Join(sp.Units, ", ")
			}
		}
		fmt.Fprintf(w, "  %s%s\n", sp.Source, units)
	}
	fmt.Fprintf(w, "\n%d rule:\n", len(s.Rules))
	for _, r := range s.Rules {
		fmt.Fprintf(w, "  %-22s %-8s %s\n", r.ID, r.Severity, r.Title)
	}
}

package app

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// Thông báo có bản SCS-Server mới (CHỈ ĐỀ XUẤT, không tự cài). Timer scs-server-update-check (6 giờ / lần,
// scripts/check-update.sh) kéo code từ GitHub và ghi file kết quả; server chỉ đọc file đó — tiến trình
// server không gọi mạng ra ngoài, không đụng repo. Cài bản mới vẫn bằng menu mục 4 / scripts/update.sh.

const (
	updateFileMax    = 512 << 10
	updateMaxCommits = 60
	updateBodyLines  = 20
	updateStaleAfter = 2 * 24 * time.Hour // timer 6 giờ / lần: quá 2 ngày chưa kiểm là timer có vấn đề
)

// UpdateCommit: một thay đổi trong bản mới.
type UpdateCommit struct {
	Hash    string    `json:"hash"`
	Date    time.Time `json:"date"`
	Subject string    `json:"subject"`
	Body    []string  `json:"body,omitempty"`
}

// UpdateInfo: kết quả lần kiểm tra gần nhất.
type UpdateInfo struct {
	Configured bool           `json:"configured"` // có file kết quả (timer đã cài và chạy ít nhất một lần)
	Available  bool           `json:"available"`  // có bản mới hơn bản đang chạy
	Running    string         `json:"running"`    // bản server đang chạy (của chính tiến trình này)
	Checked    time.Time      `json:"checked,omitzero"`
	Stale      bool           `json:"stale"`    // lần kiểm tra cuối đã cũ (timer không chạy?)
	Outdated   bool           `json:"outdated"` // kết quả tính cho bản khác (vừa cập nhật, chờ lần kiểm tra mới)
	Target     string         `json:"target,omitempty"`
	TargetDesc string         `json:"target_desc,omitempty"`
	Behind     int            `json:"behind"`
	Migrations bool           `json:"migrations"` // bản mới đổi cấu trúc DB (update.sh sẽ tự sao lưu trước)
	Error      string         `json:"error,omitempty"`
	Commits    []UpdateCommit `json:"commits"`
}

// readUpdateInfo đọc file kết quả của check-update.sh. Không có file → Configured=false.
func readUpdateInfo(path, running string, now time.Time) (UpdateInfo, error) {
	info := UpdateInfo{Running: running, Commits: []UpdateCommit{}}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return info, nil
	}
	if err != nil {
		return info, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, updateFileMax+1))
	if err != nil {
		return info, err
	}
	if len(data) > updateFileMax {
		data = data[:updateFileMax]
	}
	head, rest, _ := bytes.Cut(data, []byte("\n--\n"))
	sc := bufio.NewScanner(bytes.NewReader(head))
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), "SCS-UPDATE-CHECK 1") {
		return info, errors.New("file kết quả kiểm tra bản mới sai định dạng")
	}
	info.Configured = true
	var fileRunning string
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = clip(strings.TrimSpace(v), 200)
		switch k {
		case "checked":
			info.Checked, _ = time.Parse(time.RFC3339, v)
		case "running":
			fileRunning = v
		case "target":
			info.Target = v
		case "target_desc":
			info.TargetDesc = v
		case "behind":
			info.Behind, _ = strconv.Atoi(v)
		case "migrations":
			info.Migrations = v == "1"
		case "error":
			info.Error = v
		}
	}
	for _, rec := range bytes.Split(rest, []byte{0x1e}) {
		if len(info.Commits) >= updateMaxCommits {
			break
		}
		parts := strings.SplitN(string(rec), "\x1f", 4)
		if len(parts) < 3 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		c := UpdateCommit{Hash: clip(strings.TrimSpace(parts[0]), 40), Subject: clip(strings.TrimSpace(parts[2]), 200)}
		c.Date, _ = time.Parse(time.RFC3339, strings.TrimSpace(parts[1]))
		if len(parts) == 4 {
			for _, l := range strings.Split(parts[3], "\n") {
				l = strings.TrimSpace(l)
				low := strings.ToLower(l)
				if l == "" || strings.HasPrefix(low, "co-authored-by:") || strings.HasPrefix(low, "signed-off-by:") {
					continue
				}
				if len(c.Body) >= updateBodyLines {
					break
				}
				c.Body = append(c.Body, clip(l, 300))
			}
		}
		info.Commits = append(info.Commits, c)
	}
	info.Stale = info.Checked.IsZero() || now.Sub(info.Checked) > updateStaleAfter
	// Kết quả tính cho bản khác (server vừa được cập nhật): danh sách không còn đúng, chờ lần kiểm tra sau.
	if running != "" && fileRunning != "" && fileRunning != running {
		info.Outdated = true
		info.Commits = []UpdateCommit{}
		info.Behind = 0
	}
	info.Available = info.Behind > 0 && !info.Outdated
	return info, nil
}

// clip cắt chuỗi tối đa n ký tự và bỏ ký tự điều khiển.
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n]) + "…"
	}
	return s
}

// updateAPI: GET /admin/v1/update (quản trị gốc và mức 1).
func updateAPI(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := ""
		if d.Cfg != nil {
			path = d.Cfg.UpdateCheckFile
		}
		if path == "" {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "update": UpdateInfo{Running: d.Version, Commits: []UpdateCommit{}}})
			return
		}
		info, err := readUpdateInfo(path, d.Version, time.Now())
		if err != nil {
			d.Log.Error("không đọc được kết quả kiểm tra bản mới", "file", path, "err", err)
			info.Error = "không đọc được kết quả kiểm tra bản mới"
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "update": info})
	}
}

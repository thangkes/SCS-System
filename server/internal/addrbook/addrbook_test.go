package addrbook

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thangkes/SCS-System/common/protocol"
)

const (
	lan = "https://192.168.1.10:6222"
	wan = "https://203.0.113.5:16222"
	ddn = "https://ten.ddns.net:16222"
)

func TestSeedAndPersist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent-servers.json")
	b, err := Open(p, []string{lan + "/", "http://x:1"})
	if err != nil { // địa chỉ sai dạng trong seed bị bỏ (CleanServers), lan còn lại
		t.Fatal(err)
	}
	if got := b.URLs(); len(got) != 1 || got[0] != lan {
		t.Fatalf("seed: %v", got)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("phải ghi file từ seed:", err)
	}
	// Đã có file: seed không còn tác dụng.
	b2, err := Open(p, []string{wan})
	if err != nil {
		t.Fatal(err)
	}
	if got := b2.URLs(); len(got) != 1 || got[0] != lan {
		t.Fatalf("mở lại: %v", got)
	}
}

func TestEmptySeedNoFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.json")
	b, err := Open(p, nil)
	if err != nil || len(b.URLs()) != 0 {
		t.Fatal(err, b.URLs())
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("seed rỗng thì chưa ghi file")
	}
	if _, err := b.Add(lan, "", false); err != nil {
		t.Fatal(err)
	}
	if len(b.URLs()) != 1 {
		t.Fatal(b.URLs())
	}
}

func TestAddRemoveMove(t *testing.T) {
	b, _ := Open(filepath.Join(t.TempDir(), "a.json"), []string{lan})
	if _, err := b.Add(wan, "Kho mới", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Add(wan+"/", "", false); !errors.Is(err, ErrDuplicate) {
		t.Fatal("trùng phải bị từ chối:", err)
	}
	if _, err := b.Add("https://x", "", false); err == nil {
		t.Fatal("thiếu cổng phải bị từ chối")
	}
	if _, err := b.Add(ddn, "a‮b", false); !errors.Is(err, ErrBadNote) {
		t.Fatal("ghi chú có ký tự điều khiển phải bị từ chối:", err)
	}
	if _, err := b.Add(ddn, "DDNS", true); err != nil {
		t.Fatal(err)
	}
	if got := b.URLs(); got[0] != ddn || got[1] != lan || got[2] != wan {
		t.Fatalf("thứ tự: %v", got)
	}
	if err := b.Move(wan, -1); err != nil {
		t.Fatal(err)
	}
	if got := b.URLs(); got[1] != wan {
		t.Fatalf("sau Move: %v", got)
	}
	if err := b.Remove(lan); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(lan); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_ = b.Remove(wan)
	if err := b.Remove(ddn); !errors.Is(err, ErrLast) {
		t.Fatal("không được gỡ địa chỉ cuối:", err)
	}
	for i := len(b.URLs()); i < protocol.MaxServers; i++ {
		if _, err := b.Add("https://h"+string(rune('a'+i))+".x:1", "", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Add("https://full.x:1", "", false); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
}

func TestReloadFromOutside(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.json")
	b, _ := Open(p, []string{lan})
	other, _ := Open(p, nil) // như lệnh `scs-server servers add` chạy riêng
	if _, err := other.Add(wan, "", false); err != nil {
		t.Fatal(err)
	}
	clock := time.Now().Add(time.Minute)
	b.now = func() time.Time { return clock }
	if got := b.URLs(); len(got) != 2 {
		t.Fatalf("phải đọc lại file đã sửa từ ngoài: %v", got)
	}
	// File hỏng: giữ danh sách đang có, báo lỗi, không cho sửa.
	if err := os.WriteFile(p, []byte("{hỏng"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	if got := b.URLs(); len(got) != 2 {
		t.Fatalf("file hỏng phải giữ danh sách cũ: %v", got)
	}
	if b.Snapshot().FileErr == "" {
		t.Fatal("phải báo lỗi file")
	}
	if _, err := b.Add(ddn, "", false); err == nil {
		t.Fatal("file hỏng thì không được sửa")
	}
	if err := b.Set([]string{lan, ddn}); err != nil {
		t.Fatal("Set phải ghi đè được file hỏng:", err)
	}
	if b.Snapshot().FileErr != "" || len(b.URLs()) != 2 {
		t.Fatal(b.Snapshot())
	}
}

func TestCorruptAtStart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.json")
	os.WriteFile(p, []byte(`{"entries":[{"url":"http://x"}]}`), 0o600)
	b, err := Open(p, []string{lan})
	if err == nil || b == nil {
		t.Fatal("file sai phải báo lỗi nhưng vẫn trả Book")
	}
	if got := b.URLs(); len(got) != 1 || got[0] != lan {
		t.Fatalf("tạm dùng seed: %v", got)
	}
}

func TestSnapshot(t *testing.T) {
	b, _ := Open(filepath.Join(t.TempDir(), "a.json"), []string{lan})
	oldHash := protocol.ServersHash(b.URLs())
	b.Add(wan, "", false)
	cur := protocol.ServersHash(b.URLs())
	b.Report(1, cur, wan)
	b.Report(2, oldHash, lan)
	b.Report(3, "", lan)                // agent cũ
	b.Report(4, cur, "https://old.x:1") // địa chỉ đã gỡ / lúc cài
	b.Report(5, cur, "javascript:alert(1)")
	st := b.Snapshot()
	if st.Online != 5 || st.Synced != 3 || st.Previous != 1 || st.Unknown != 1 {
		t.Fatalf("%+v", st)
	}
	if len(st.Pending) != 1 || st.Pending[0] != 2 {
		t.Fatalf("pending %v", st.Pending)
	}
	if st.Addrs[0].URL != lan || st.Addrs[0].Using != 2 || st.Addrs[1].Using != 1 {
		t.Fatalf("%+v", st.Addrs)
	}
	if len(st.Addrs) != 3 || st.Addrs[2].InList || st.Addrs[2].URL != "https://old.x:1" {
		t.Fatalf("địa chỉ ngoài danh sách: %+v", st.Addrs)
	}
	if b.UsingCount(lan) != 2 {
		t.Fatal(b.UsingCount(lan))
	}
	// Máy không gọi trong Window thì không tính.
	later := time.Now().Add(Window + time.Minute)
	b.now = func() time.Time { return later }
	if st := b.Snapshot(); st.Online != 0 {
		t.Fatalf("%+v", st)
	}
}

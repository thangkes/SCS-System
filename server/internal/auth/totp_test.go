package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// RFC 6238 phụ lục B (SHA-1, khóa "12345678901234567890"), lấy 6 số cuối của mã 8 số.
func TestTOTPRFCVectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for _, v := range []struct {
		t    int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		if got := totpCode(key, v.t/30); got != v.want {
			t.Errorf("T=%d: %s, muốn %s", v.t, got, v.want)
		}
	}
}

func TestTOTPMatchSkewAndReplay(t *testing.T) {
	key := newTOTPKey()
	now := time.Unix(1_800_000_000, 0)
	cur := totpStep(now)
	for _, d := range []int64{-1, 0, 1} {
		if s, ok := totpMatch(key, totpCode(key, cur+d), now, 0); !ok || s != cur+d {
			t.Fatalf("lệch %d bước phải nhận: %v %d", d, ok, s)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := totpMatch(key, totpCode(key, cur+d), now, 0); ok {
			t.Fatalf("lệch %d bước không được nhận", d)
		}
	}
	if _, ok := totpMatch(key, totpCode(key, cur), now, cur); ok {
		t.Fatal("mã ở bước đã dùng không được nhận lại")
	}
	if _, ok := totpMatch(key, "12345", now, 0); ok {
		t.Fatal("mã sai độ dài")
	}
}

func TestTOTPSealer(t *testing.T) {
	s, err := NewTOTPSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sec := newTOTPKey()
	box := s.seal("10123", sec)
	if got, err := s.open("10123", box); err != nil || !bytes.Equal(got, sec) {
		t.Fatalf("giải mã: %v", err)
	}
	if _, err := s.open("10456", box); err == nil {
		t.Fatal("bản mã chép sang tài khoản khác không được giải mã")
	}
	box[len(box)-1] ^= 1
	if _, err := s.open("10123", box); err == nil {
		t.Fatal("bản mã bị sửa không được giải mã")
	}
	if _, err := NewTOTPSealer([]byte("ngan")); err == nil {
		t.Fatal("khóa sai độ dài")
	}
	f := filepath.Join(t.TempDir(), "totp.key")
	os.WriteFile(f, []byte(strings.Repeat("ab", 32)+"\n"), 0o600)
	if _, err := LoadTOTPSealer(f); err != nil {
		t.Fatalf("file khóa hợp lệ: %v", err)
	}
	os.WriteFile(f, []byte("khong-phai-hex"), 0o600)
	if _, err := LoadTOTPSealer(f); err == nil {
		t.Fatal("file khóa sai phải báo lỗi")
	}
}

// Ví dụ chuẩn "HELLO WORLD" 1-M: 16 byte dữ liệu → 10 byte sửa lỗi đã biết.
func TestQRReedSolomon(t *testing.T) {
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	if got := rsECC(data, 10); !bytes.Equal(got, want) {
		t.Fatalf("RS: %v, muốn %v", got, want)
	}
}

func TestQRFormatAndVersionBits(t *testing.T) {
	if got := bch(7, 0x1f25, 12); got != 0x07C94 {
		t.Fatalf("thông tin phiên bản 7: %#x", got)
	}
	if got := bch(10, 0x1f25, 12); got != 0x0A4D3 {
		t.Fatalf("thông tin phiên bản 10: %#x", got)
	}
	// Mức M mặt nạ 0..7 (bảng chuẩn).
	want := []uint{0x5412, 0x5125, 0x5E7C, 0x5B4B, 0x45F9, 0x40CE, 0x4F97, 0x4AA0}
	for mask, w := range want {
		if got := bch(uint(mask), 0x537, 10) ^ 0x5412; got != w {
			t.Fatalf("định dạng M mặt nạ %d: %#x, muốn %#x", mask, got, w)
		}
	}
}

func TestQRSizes(t *testing.T) {
	for _, c := range []struct{ n, size int }{{10, 21}, {14, 21}, {15, 25}, {106, 41}, {107, 45}, {213, 57}} {
		q, err := EncodeQR(bytes.Repeat([]byte("a"), c.n))
		if err != nil || q.Size != c.size {
			t.Fatalf("%d byte: %v, muốn cỡ %d", c.n, err, c.size)
		}
	}
	if _, err := EncodeQR(bytes.Repeat([]byte("a"), 214)); err == nil {
		t.Fatal("quá sức chứa phải báo lỗi")
	}
	uri := totpURI(strings.Repeat("9", 32), newTOTPKey())
	q, err := EncodeQR([]byte(uri))
	if err != nil {
		t.Fatalf("chuỗi otpauth dài nhất phải vừa: %d byte", len(uri))
	}
	u := q.SVGDataURL(4)
	if _, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(u, "data:image/svg+xml;base64,")); err != nil ||
		!strings.HasPrefix(u, "data:image/svg+xml;base64,") {
		t.Fatalf("data URL: %v", err)
	}
}

// SCS_QR_DUMP=<thư mục>: ghi PNG các mã QR mẫu để đọc thử bằng bộ đọc QR thật (kiểm tay).
func TestQRDump(t *testing.T) {
	dir := os.Getenv("SCS_QR_DUMP")
	if dir == "" {
		t.Skip("đặt SCS_QR_DUMP để ghi PNG")
	}
	samples := map[string]string{
		"v1":   "hello",
		"v3":   "otpauth://totp/SCS:1?secret=A",
		"uri":  totpURI("10123", []byte("12345678901234567890")),
		"long": totpURI(strings.Repeat("9", 32), []byte("12345678901234567890")),
		"v7":   strings.Repeat("abcdefghij", 12),
		"v8":   strings.Repeat("abcdefghij", 14),
		"v9":   strings.Repeat("abcdefghij", 17),
		"v10":  strings.Repeat("0123456789", 21),
	}
	for name, s := range samples {
		q, err := EncodeQR([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		const sc, quiet = 8, 4
		w := (q.Size + 2*quiet) * sc
		img := image.NewGray(image.Rect(0, 0, w, w))
		for y := 0; y < w; y++ {
			for x := 0; x < w; x++ {
				mx, my := x/sc-quiet, y/sc-quiet
				c := color.Gray{255}
				if mx >= 0 && my >= 0 && mx < q.Size && my < q.Size && q.Dark(mx, my) {
					c = color.Gray{0}
				}
				img.Set(x, y, c)
			}
		}
		f, _ := os.Create(filepath.Join(dir, name+".png"))
		png.Encode(f, img)
		f.Close()
		os.WriteFile(filepath.Join(dir, name+".txt"), []byte(s), 0o644)
	}
}

// memTOTP: kho khóa TOTP trong bộ nhớ.
type memTOTP struct {
	mu   sync.Mutex
	box  map[string][]byte
	last map[string]int64
}

func newMemTOTP() *memTOTP { return &memTOTP{box: map[string][]byte{}, last: map[string]int64{}} }

func (s *memTOTP) TOTPSecret(_ context.Context, u string) ([]byte, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.box[u], s.last[u], nil
}
func (s *memTOTP) SetTOTP(_ context.Context, u string, box []byte, step int64, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.box[u], s.last[u] = box, step
	return nil
}
func (s *memTOTP) ClearTOTP(_ context.Context, u, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.box, u)
	s.last[u] = 0
	return nil
}
func (s *memTOTP) UseTOTPStep(_ context.Context, u string, step int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.box[u] == nil || s.last[u] >= step {
		return false, nil
	}
	s.last[u] = step
	return true, nil
}

func (s *memTOTP) get(u string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.box[u]
}

func TestTOTPFlow(t *testing.T) {
	m, h, _ := newTestManager(t)
	st, keys := newMemAccounts(), newMemTOTP()
	m.Accounts, m.TOTPKeys = st, keys
	m.TOTP, _ = NewTOTPSealer(bytes.Repeat([]byte{1}, 32))
	if !m.TOTPEnabled() || m.EmailEnabled() {
		t.Fatal("chỉ bật app xác thực, không có mail")
	}
	var opt map[string]any
	_ = json.Unmarshal(req(h, "GET", "/auth/options", "", nil).Body.Bytes(), &opt)
	if opt["totp"] != true || opt["email"] != false {
		t.Fatalf("options: %v", opt)
	}

	// Đăng ký không cần email → chờ duyệt; đăng ký trùng trả cùng câu.
	if code, _, _ := post(h, "/auth/totp/register", accountReq{Username: "abc"}); code != 400 {
		t.Fatalf("tên không phải số: %d", code)
	}
	_, j1, _ := post(h, "/auth/totp/register", accountReq{Username: "10123"})
	_, j2, _ := post(h, "/auth/totp/register", accountReq{Username: "10123"})
	if j1["message"] == nil || j1["message"] != j2["message"] {
		t.Fatalf("đăng ký trùng phải trả cùng câu: %v / %v", j1, j2)
	}
	post(h, "/auth/totp/register", accountReq{Username: "10456"})
	if a, err := st.AccountByName(context.Background(), "10456"); err != nil || a.Status != AccountPending || a.Email != "" {
		t.Fatalf("tài khoản thứ hai không email phải tạo được: %+v %v", a, err)
	}

	root := rootCookie(t, m, h)
	if code, _, _ := post(h, "/admin/v1/accounts/10123", map[string]any{"action": "totp_setup"}, root); code != 409 {
		t.Fatalf("chưa duyệt không được tạo mã cài đặt: %d", code)
	}
	post(h, "/admin/v1/accounts/10123", map[string]any{"action": "approve", "level": 5}, root)
	code, j, _ := post(h, "/admin/v1/accounts/10123", map[string]any{"action": "totp_setup"}, root)
	setup, _ := j["setup_code"].(string)
	if code != 200 || len(setup) != 8 {
		t.Fatalf("tạo mã cài đặt: %d %v", code, j)
	}
	// Chưa cài app: đăng nhập không được.
	if code, _, _ := post(h, "/auth/totp/login", accountReq{Username: "10123", Code: "123456"}); code != 401 {
		t.Fatalf("chưa cài app: %d", code)
	}
	// Mã cài đặt sai: hủy luôn mã (một lần).
	wrong := "00000000"
	if setup == wrong {
		wrong = "11111111"
	}
	if code, _, _ := post(h, "/auth/totp/setup", accountReq{Username: "10123", Code: wrong}); code != 401 {
		t.Fatalf("mã cài đặt sai: %d", code)
	}
	if code, _, _ := post(h, "/auth/totp/setup", accountReq{Username: "10123", Code: setup}); code != 401 {
		t.Fatalf("mã cài đặt đã bị hủy vẫn dùng được: %d", code)
	}
	_, j, _ = post(h, "/admin/v1/accounts/10123", map[string]any{"action": "totp_setup"}, root)
	setup = j["setup_code"].(string)
	code, j, _ = post(h, "/auth/totp/setup", accountReq{Username: "10123", Code: setup})
	if code != 200 || !strings.HasPrefix(j["qr"].(string), "data:image/svg+xml;base64,") {
		t.Fatalf("mở trang QR: %d %v", code, j)
	}
	token := j["token"].(string)
	secret, err := b32.DecodeString(strings.ReplaceAll(j["secret"].(string), " ", ""))
	if err != nil || len(secret) != totpKeySize {
		t.Fatalf("khóa dạng chữ: %v", err)
	}
	if code, _, _ := post(h, "/auth/totp/setup", accountReq{Username: "10123", Code: setup}); code != 401 {
		t.Fatalf("mã cài đặt dùng lần hai: %d", code)
	}
	// Xác nhận sai rồi đúng → đăng nhập luôn.
	now := time.Now()
	bad := "000000"
	if totpCode(secret, totpStep(now)) == bad || totpCode(secret, totpStep(now)-1) == bad || totpCode(secret, totpStep(now)+1) == bad {
		bad = "999999"
	}
	if code, _, _ := post(h, "/auth/totp/confirm", map[string]string{"token": token, "code": bad}); code != 401 {
		t.Fatalf("mã xác nhận sai: %d", code)
	}
	code, j, cs := post(h, "/auth/totp/confirm", map[string]string{"token": token, "code": totpCode(secret, totpStep(now))})
	if code != 200 || sessionCookie(cs) == nil || j["name"] != "10123" {
		t.Fatalf("xác nhận: %d %v", code, j)
	}
	if code, _, _ := post(h, "/auth/totp/confirm", map[string]string{"token": token, "code": totpCode(secret, totpStep(now))}); code != 410 {
		t.Fatalf("token đã dùng: %d", code)
	}
	box := keys.get("10123")
	if box == nil || bytes.Contains(box, secret) {
		t.Fatal("khóa phải được lưu và đã mã hóa")
	}
	if a, _ := st.AccountByName(context.Background(), "10123"); a.Status != AccountActive {
		t.Fatalf("tài khoản: %+v", a)
	}
	// Mã vừa dùng để xác nhận không dùng lại để đăng nhập.
	if code, _, _ := post(h, "/auth/totp/login", accountReq{Username: "10123", Code: totpCode(secret, totpStep(now))}); code != 401 {
		t.Fatalf("mã đã dùng: %d", code)
	}
	// Mã bước kế tiếp (trong ±1 bước) đăng nhập được, nhưng chỉ một lần.
	next := totpCode(secret, totpStep(now)+1)
	code, j, cs = post(h, "/auth/totp/login", accountReq{Username: "10123", Code: next})
	if code != 200 || sessionCookie(cs) == nil || j["level"] != float64(LevelIncidents) {
		t.Fatalf("đăng nhập: %d %v", code, j)
	}
	if code, _, _ := post(h, "/auth/totp/login", accountReq{Username: "10123", Code: next}); code != 401 {
		t.Fatalf("dùng lại mã: %d", code)
	}
	// Đặt lại: xóa khóa, cắt phiên.
	sess := sessionCookie(cs)
	if code, _, _ := post(h, "/admin/v1/accounts/10123", map[string]any{"action": "totp_reset"}, root); code != 200 {
		t.Fatalf("đặt lại: %d", code)
	}
	if keys.get("10123") != nil {
		t.Fatal("khóa phải bị xóa")
	}
	if w := req(h, "GET", "/auth/me", "", nil, sess); w.Code != 401 {
		t.Fatalf("phiên cũ phải bị cắt: %d", w.Code)
	}
	if code, _, _ := post(h, "/auth/totp/login", accountReq{Username: "10123", Code: totpCode(secret, totpStep(time.Now())+1)}); code != 401 {
		t.Fatalf("sau khi đặt lại không đăng nhập được bằng app cũ: %d", code)
	}
}

func TestTOTPOffWithoutKey(t *testing.T) {
	m, h, _ := newTestManager(t)
	m.Accounts = newMemAccounts()
	if m.TOTPEnabled() {
		t.Fatal("thiếu khóa thì phải tắt")
	}
	if code, _, _ := post(h, "/auth/totp/login", accountReq{Username: "10123", Code: "123456"}); code != 503 {
		t.Fatalf("tắt: %d", code)
	}
}

func TestTOTPLoginLimited(t *testing.T) {
	m, h, _ := newTestManager(t)
	m.Accounts, m.TOTPKeys = newMemAccounts(), newMemTOTP()
	m.TOTP, _ = NewTOTPSealer(bytes.Repeat([]byte{1}, 32))
	limited := false
	for i := 0; i < totpAttemptsPerAcc+2; i++ {
		code, _, _ := post(h, "/auth/totp/login", accountReq{Username: "10999", Code: "123456"})
		if code == 429 || code == 403 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("nhập mã nhiều lần phải bị chặn")
	}
}

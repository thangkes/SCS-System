package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Bộ tạo mã QR tối giản (ISO/IEC 18004) chỉ đủ cho chuỗi otpauth:// của app xác thực: chế độ byte, mức sửa
// lỗi M, phiên bản 1–10 (tối đa 213 byte). Tự viết để không thêm thư viện ngoài và không gọi dịch vụ tạo QR
// trên Internet (lộ khóa bí mật). Kiểm bằng bộ đọc QR thật trong test (qr_test.go) và đối chiếu bảng chuẩn.

type qrVersion struct {
	ecPerBlock int
	blocks     [][2]int // {số khối, số byte dữ liệu mỗi khối}
	align      []int
}

// Bảng mức M, phiên bản 1–10 (ISO/IEC 18004 bảng 9 và phụ lục E).
var qrVersions = [...]qrVersion{
	1:  {10, [][2]int{{1, 16}}, nil},
	2:  {16, [][2]int{{1, 28}}, []int{6, 18}},
	3:  {26, [][2]int{{1, 44}}, []int{6, 22}},
	4:  {18, [][2]int{{2, 32}}, []int{6, 26}},
	5:  {24, [][2]int{{2, 43}}, []int{6, 30}},
	6:  {16, [][2]int{{4, 27}}, []int{6, 34}},
	7:  {18, [][2]int{{4, 31}}, []int{6, 22, 38}},
	8:  {22, [][2]int{{2, 38}, {2, 39}}, []int{6, 24, 42}},
	9:  {22, [][2]int{{3, 36}, {2, 37}}, []int{6, 26, 46}},
	10: {26, [][2]int{{4, 43}, {1, 44}}, []int{6, 28, 50}},
}

func (v qrVersion) dataBytes() int {
	n := 0
	for _, b := range v.blocks {
		n += b[0] * b[1]
	}
	return n
}

// ---- GF(256), đa thức 0x11d
var gfExp, gfLog [512]byte

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsECC: n byte sửa lỗi Reed–Solomon của data.
func rsECC(data []byte, n int) []byte {
	gen := []byte{1}
	for i := 0; i < n; i++ {
		next := make([]byte, len(gen)+1)
		for j, c := range gen {
			next[j] ^= c
			next[j+1] ^= gfMul(c, gfExp[i])
		}
		gen = next
	}
	rem := make([]byte, n)
	for _, d := range data {
		f := d ^ rem[0]
		copy(rem, rem[1:])
		rem[n-1] = 0
		for j := 0; j < n; j++ {
			rem[j] ^= gfMul(gen[j+1], f)
		}
	}
	return rem
}

type bitBuf struct{ b []byte }

func (bb *bitBuf) put(v uint, n int) {
	for i := n - 1; i >= 0; i-- {
		bb.b = append(bb.b, byte(v>>uint(i))&1)
	}
}

// qrCodewords: chuỗi codeword cuối cùng (dữ liệu + sửa lỗi, đã xen kẽ khối).
func qrCodewords(data []byte, ver int) []byte {
	v := qrVersions[ver]
	cc := 8
	if ver >= 10 {
		cc = 16
	}
	var bb bitBuf
	bb.put(0b0100, 4)
	bb.put(uint(len(data)), cc)
	for _, c := range data {
		bb.put(uint(c), 8)
	}
	capBits := v.dataBytes() * 8
	bb.put(0, min(4, capBits-len(bb.b)))
	for len(bb.b)%8 != 0 {
		bb.b = append(bb.b, 0)
	}
	bytes := make([]byte, 0, v.dataBytes())
	for i := 0; i < len(bb.b); i += 8 {
		var c byte
		for j := 0; j < 8; j++ {
			c = c<<1 | bb.b[i+j]
		}
		bytes = append(bytes, c)
	}
	for pad := []byte{0xEC, 0x11}; len(bytes) < v.dataBytes(); {
		bytes = append(bytes, pad[(len(bytes)-len(bb.b)/8)%2])
	}
	var blocks, eccs [][]byte
	off := 0
	for _, g := range v.blocks {
		for i := 0; i < g[0]; i++ {
			blk := bytes[off : off+g[1]]
			off += g[1]
			blocks = append(blocks, blk)
			eccs = append(eccs, rsECC(blk, v.ecPerBlock))
		}
	}
	out := make([]byte, 0, off+len(blocks)*v.ecPerBlock)
	for i := 0; ; i++ {
		any := false
		for _, blk := range blocks {
			if i < len(blk) {
				out = append(out, blk[i])
				any = true
			}
		}
		if !any {
			break
		}
	}
	for i := 0; i < v.ecPerBlock; i++ {
		for _, e := range eccs {
			out = append(out, e[i])
		}
	}
	return out
}

// QR là ma trận mô-đun (true = ô đen).
type QR struct {
	Size    int
	modules [][]bool
	fixed   [][]bool
}

func (q *QR) Dark(x, y int) bool { return q.modules[y][x] }

func (q *QR) set(x, y int, dark bool) {
	q.modules[y][x] = dark
	q.fixed[y][x] = true
}

// ErrQRTooLong: chuỗi dài hơn sức chứa phiên bản 10 mức M.
var ErrQRTooLong = errors.New("chuỗi quá dài cho mã QR")

// EncodeQR tạo mã QR mức sửa lỗi M cho data (chọn phiên bản nhỏ nhất vừa).
func EncodeQR(data []byte) (*QR, error) {
	ver := 0
	for v := 1; v <= 10; v++ {
		cc := 8
		if v >= 10 {
			cc = 16
		}
		if 4+cc+8*len(data) <= qrVersions[v].dataBytes()*8 {
			ver = v
			break
		}
	}
	if ver == 0 {
		return nil, ErrQRTooLong
	}
	cw := qrCodewords(data, ver)
	best, bestScore := (*QR)(nil), -1
	for mask := 0; mask < 8; mask++ {
		q := newQRFrame(ver)
		q.place(cw, mask)
		q.format(mask)
		if s := q.penalty(); best == nil || s < bestScore {
			best, bestScore = q, s
		}
	}
	return best, nil
}

func newQRFrame(ver int) *QR {
	n := 17 + 4*ver
	q := &QR{Size: n, modules: make([][]bool, n), fixed: make([][]bool, n)}
	for i := range q.modules {
		q.modules[i] = make([]bool, n)
		q.fixed[i] = make([]bool, n)
	}
	finder := func(cx, cy int) {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := cx+dx, cy+dy
				if x < 0 || y < 0 || x >= n || y >= n {
					continue
				}
				d := max(abs(dx), abs(dy))
				q.set(x, y, d != 2 && d != 4)
			}
		}
	}
	finder(3, 3)
	finder(n-4, 3)
	finder(3, n-4)
	for i := 8; i < n-8; i++ {
		q.set(i, 6, i%2 == 0)
		q.set(6, i, i%2 == 0)
	}
	al := qrVersions[ver].align
	for _, ay := range al {
		for _, ax := range al {
			if (ax == 6 && ay == 6) || (ax == 6 && ay == al[len(al)-1]) || (ay == 6 && ax == al[len(al)-1]) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					q.set(ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	// Chừa chỗ thông tin định dạng (ghi sau khi chọn mặt nạ) và ô đen cố định.
	for i := 0; i < 9; i++ {
		q.fixed[8][i], q.fixed[i][8] = true, true
	}
	for i := 0; i < 8; i++ {
		q.fixed[8][n-1-i], q.fixed[n-1-i][8] = true, true
	}
	q.set(8, n-8, true)
	if ver >= 7 {
		bits := bch(uint(ver), 0x1f25, 12)
		for i := 0; i < 18; i++ {
			dark := (bits>>uint(i))&1 == 1
			a, b := n-11+i%3, i/3
			q.set(a, b, dark)
			q.set(b, a, dark)
		}
	}
	return q
}

// bch: value << deg nối với phần dư khi chia cho gen (mã BCH của thông tin định dạng / phiên bản).
func bch(value, gen uint, deg int) uint {
	r := value << uint(deg)
	gl := 0
	for g := gen; g > 0; g >>= 1 {
		gl++
	}
	for i := 0; ; i++ {
		rl := 0
		for x := r; x > 0; x >>= 1 {
			rl++
		}
		if rl < gl {
			break
		}
		r ^= gen << uint(rl-gl)
	}
	return value<<uint(deg) | r
}

func qrMask(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (y/2+x/3)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	default:
		return ((x+y)%2+x*y%3)%2 == 0
	}
}

// place: xếp codeword theo đường zig-zag từ góc dưới phải, áp mặt nạ.
func (q *QR) place(cw []byte, mask int) {
	n := q.Size
	bit := 0
	total := len(cw) * 8
	up := true
	for col := n - 1; col > 0; col -= 2 {
		if col == 6 {
			col--
		}
		for i := 0; i < n; i++ {
			y := i
			if up {
				y = n - 1 - i
			}
			for dx := 0; dx < 2; dx++ {
				x := col - dx
				if q.fixed[y][x] {
					continue
				}
				dark := false
				if bit < total {
					dark = (cw[bit/8]>>uint(7-bit%8))&1 == 1
				}
				bit++
				q.modules[y][x] = dark != qrMask(mask, x, y)
			}
		}
		up = !up
	}
}

// format ghi 15 bit thông tin định dạng (mức M = 00) hai nơi.
func (q *QR) format(mask int) {
	n := q.Size
	bits := bch(uint(0b00<<3|mask), 0x537, 10) ^ 0x5412
	b := func(i int) bool { return (bits>>uint(i))&1 == 1 }
	for i := 0; i <= 5; i++ {
		q.modules[i][8] = b(i)
	}
	q.modules[7][8] = b(6)
	q.modules[8][8] = b(7)
	q.modules[8][7] = b(8)
	for i := 9; i < 15; i++ {
		q.modules[8][14-i] = b(i)
	}
	for i := 0; i < 8; i++ {
		q.modules[8][n-1-i] = b(i)
	}
	for i := 8; i < 15; i++ {
		q.modules[n-15+i][8] = b(i)
	}
	q.modules[n-8][8] = true
}

// penalty: điểm phạt chọn mặt nạ (ISO/IEC 18004 mục 8.8.2).
func (q *QR) penalty() int {
	n, s := q.Size, 0
	for pass := 0; pass < 2; pass++ {
		for a := 0; a < n; a++ {
			run, prev := 0, false
			var line []bool
			for b := 0; b < n; b++ {
				d := q.modules[a][b]
				if pass == 1 {
					d = q.modules[b][a]
				}
				line = append(line, d)
				if b > 0 && d == prev {
					run++
				} else {
					if run >= 5 {
						s += run - 2
					}
					run = 1
				}
				prev = d
			}
			if run >= 5 {
				s += run - 2
			}
			for b := 0; b+7 <= n; b++ {
				if line[b] && !line[b+1] && line[b+2] && line[b+3] && line[b+4] && !line[b+5] && line[b+6] {
					before := b >= 4 && !line[b-1] && !line[b-2] && !line[b-3] && !line[b-4]
					after := b+11 <= n && !line[b+7] && !line[b+8] && !line[b+9] && !line[b+10]
					if before || after {
						s += 40
					}
				}
			}
		}
	}
	dark := 0
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if q.modules[y][x] {
				dark++
			}
			if x+1 < n && y+1 < n {
				c := q.modules[y][x]
				if q.modules[y][x+1] == c && q.modules[y+1][x] == c && q.modules[y+1][x+1] == c {
					s += 3
				}
			}
		}
	}
	pct := dark * 100 / (n * n)
	s += abs(pct-50) / 5 * 10
	return s
}

// SVG: mã QR dạng SVG (lề 4 ô, mỗi ô `scale` px).
func (q *QR) SVG(scale int) string {
	const quiet = 4
	w := (q.Size + 2*quiet) * scale
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" shape-rendering="crispEdges">`,
		w, w, q.Size+2*quiet, q.Size+2*quiet)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="#fff"/><path fill="#000" d="`)
	for y := 0; y < q.Size; y++ {
		for x := 0; x < q.Size; x++ {
			if q.modules[y][x] {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

// SVGDataURL: SVG dạng data: URL cho thẻ <img> (CSP của web cho phép img-src data:).
func (q *QR) SVGDataURL(scale int) string {
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(q.SVG(scale)))
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

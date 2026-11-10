# Hướng dẫn viết rule quét log

Tài liệu cho người viết rule của SCS-System: rule là gì, viết thế nào, kiểm và chạy thử ra sao, rồi đưa xuống máy agent.

Mục lục

1. [Rule chạy ở đâu](#1-rule-chạy-ở-đâu)
2. [Quy trình 6 bước](#2-quy-trình-6-bước)
3. [Cấu trúc file rule](#3-cấu-trúc-file-rule)
4. [Từng trường của một rule](#4-từng-trường-của-một-rule)
5. [Chọn nguồn log: file hay journald](#5-chọn-nguồn-log-file-hay-journald)
6. [Viết regex cho match / exclude](#6-viết-regex-cho-match--exclude)
7. [Đếm ngưỡng: aggregate, group_by, cooldown_s](#7-đếm-ngưỡng-aggregate-group_by-cooldown_s)
8. [Chọn severity](#8-chọn-severity)
9. [Viết tests cho rule](#9-viết-tests-cho-rule)
10. [Các lệnh](#10-các-lệnh)
11. [Ví dụ làm từ đầu](#11-ví-dụ-làm-từ-đầu)
12. [Lỗi hay gặp](#12-lỗi-hay-gặp)
13. [Giới hạn](#13-giới-hạn)
14. [An toàn](#14-an-toàn)
15. [Hiện trạng: đã có / chưa có](#15-hiện-trạng-đã-có--chưa-có)

---

## 1. Rule chạy ở đâu

Server và agent dùng **cùng một khung rule** (package `common/rules`): cùng định nghĩa, cùng hàm kiểm tra, cùng bộ máy khớp. Vì vậy một rule đã qua `scs-server rules check` / `rules test` trên server sẽ chạy y hệt trên máy agent.

```
 admin viết            server                               agent (máy tại điểm bán)
 ───────────           ──────────────────────────           ─────────────────────────────────
 file .yaml  ──▶  rules check   kiểm cú pháp + tests
                  rules test    chạy thử trên log mẫu
                  rules build   bỏ tests, ký bằng      ──▶  kiểm chữ ký (release.pub)
                                khóa phát hành              kiểm lại y như server
                                → file .json                đọc log → khớp rule → sự kiện
                                                            gửi sự kiện về server
```

- **Server**: đọc file YAML do admin viết, kiểm, chạy thử, ký thành *bộ rule đã ký* (file JSON).
- **Agent**: chỉ nhận bộ rule đã ký. Chữ ký sai hoặc nội dung sai thì bỏ, giữ bộ đang dùng.
- Agent **không gửi cả file log** về server, chỉ gửi những dòng khớp rule (kèm số lần đếm, các trường lấy ra).

Code:

| Phần | Vị trí |
|---|---|
| Định nghĩa, kiểm tra, bộ máy khớp, ký | `common/rules/` |
| Đọc YAML (server) | `server/internal/rulefile/` |
| Lệnh `scs-server rules` | `server/cmd/scs-server/rules.go` |
| Bộ rule mẫu | `server/rules/*.yaml` |
| Nạp bộ rule, chạy rule (agent) | `agent/internal/scan/` |
| Lệnh `scs-agent rules` | `agent/cmd/scs-agent/rules.go` |

## 2. Quy trình 6 bước

1. **Lấy log mẫu thật** từ một máy agent (xem [mục 5](#5-chọn-nguồn-log-file-hay-journald)), chép về máy server. Nhớ lấy cả dòng *nên khớp* và dòng *trông giống nhưng không được khớp*.
2. **Viết rule** vào file YAML trong **thư mục rule riêng của bạn** (ví dụ `~/scs-rules/`, xem bên dưới). Sao chép một rule có sẵn trong `mau-co-ban.yaml` rồi sửa là nhanh nhất.
3. **Thêm tests**: dán vài dòng log thật vào `tests.match` và `tests.no_match`.
4. **Kiểm**: `scs-server rules check ~/scs-rules/ten-bo.yaml` (cú pháp, giới hạn, tests).
5. **Chạy thử trên log mẫu**: `scs-server rules test ~/scs-rules/ten-bo.yaml -log mau.log`. Xem rule có báo quá nhiều hay quá ít không, chỉnh `aggregate` / `exclude`.
6. **Tăng `version`, ký và đưa xuống agent**: `scs-server rules build ... -o ...json`, rồi (hiện tại) cài tay trên máy agent bằng `scs-agent rules -install`. Sau này server tự phát cho agent qua heartbeat, xem [mục 15](#15-hiện-trạng-đã-có--chưa-có).

### Để file rule ở đâu (để cập nhật server không bị vướng)

**Không sửa file trong thư mục code** `~/SCS-System/server/rules/`. Thư mục đó là bộ rule **mẫu** đi kèm code:

- `update.sh` (menu mục **4**) **từ chối cập nhật** khi có file trong thư mục code bị sửa mà chưa commit (báo "Thư mục code có thay đổi chưa commit").
- File mới bạn tự thêm vào đó không chặn cập nhật, nhưng nếu bản sau có file mẫu trùng tên thì `git` không kéo về được.
- Bộ mẫu có thể được sửa ở bản mới; giữ bộ của bạn riêng thì không bị đè.

Cách làm, chỉ một lần trên máy server:

```bash
mkdir -p ~/scs-rules
cp ~/SCS-System/server/rules/mau-co-ban.yaml ~/scs-rules/cong-ty.yaml
nano ~/scs-rules/cong-ty.yaml        # đổi dòng name: thành  name: cong-ty  rồi sửa / thêm rule
```

Sau mỗi lần cập nhật server, xem bộ mẫu có rule mới đáng chép sang không:

```bash
diff ~/SCS-System/server/rules/mau-co-ban.yaml ~/scs-rules/cong-ty.yaml
```

Nên giữ bản sao `~/scs-rules/` ở nơi khác (hoặc tự `git init` trong thư mục đó) để xem lại ai sửa gì, khi nào.

## 3. Cấu trúc file rule

Một file = một **bộ rule**. Bộ rule gán cho nhóm máy (sau này: cấp chung / siêu thị / máy, giống cấu hình agent).

```yaml
name: mau-co-ban                # tên bộ: chữ thường, số, . _ -
version: '2026.11.10-01'        # LUÔN để trong nháy đơn; đổi mỗi lần sửa
description: Rule cơ bản        # tùy chọn

rules:
  - id: SSH-FAIL-01
    title: Đăng nhập SSH sai nhiều lần từ một IP
    severity: medium
    source: /var/log/auth.log
    match: 'sshd\[\d+\]: Failed password for (?:invalid user )?(?P<user>\S+) from (?P<ip>[0-9a-fA-F.:]+)'
    aggregate: { window_s: 300, threshold: 5, group_by: ip }
    cooldown_s: 1800
    tests:
      match:
        - 'Nov 10 08:01:02 12345-TN1-PRI sshd[2211]: Failed password for root from 10.0.5.20 port 50122 ssh2'
      no_match:
        - 'Nov 10 08:01:02 12345-TN1-PRI sshd[2211]: Accepted password for user from 10.0.5.20 port 50122 ssh2'
```

Quy tắc YAML cần nhớ:

- Thụt lề bằng **dấu cách**, không dùng tab. Mỗi cấp 2 dấu cách.
- Mỗi rule bắt đầu bằng `- id: ...`.
- Regex và dòng log mẫu nên để trong **nháy đơn** `'...'`. Trong nháy đơn, `\` giữ nguyên (đúng thứ regex cần); muốn viết dấu nháy đơn thì gõ hai lần `''`.
- `version` luôn để trong nháy: `2026.11` không nháy sẽ bị YAML hiểu là số thực và báo lỗi.
- Gõ sai tên trường (ví dụ `serverity`) là **lỗi**, không bị bỏ qua lặng lẽ.

**Phiên bản (`version`)**: agent chỉ tải lại bộ rule khi phiên bản khác bản đang chạy. Quy ước: `năm.tháng.ngày-số thứ tự`, ví dụ `'2026.11.10-01'`, sửa lần hai trong ngày thì `'2026.11.10-02'`. Chỉ dùng chữ, số và `. _ + -`.

## 4. Từng trường của một rule

| Trường | Bắt buộc | Ý nghĩa |
|---|---|---|
| `id` | có | Mã rule, **không đổi** sau khi đã dùng (sự kiện và cảnh báo trên server gắn theo mã này). Chữ IN HOA, số, nối bằng `-`, tối đa 48 ký tự. Quy ước `NHÓM-VIỆC-SỐ`: `SSH-FAIL-01`, `DISK-IO-ERR-01`, `POS-SVC-DOWN-01`. |
| `title` | nên có | Mô tả ngắn, hiện trên web (tối đa 120 ký tự). |
| `severity` | có | `info`, `low`, `medium`, `high`, `critical`, xem [mục 8](#8-chọn-severity). |
| `source` | có | Đường dẫn file log tuyệt đối trong `/var/log/`, hoặc `journald`. |
| `unit` | không | Chỉ dùng với `journald`: tên unit systemd hoặc `kernel`. Bỏ trống = mọi dòng journal (tốn hơn, nên tránh). |
| `match` | có | Regex. Dòng khớp là "trúng". Nhóm có tên `(?P<ten>...)` thành trường của sự kiện. |
| `exclude` | không | Regex. Dòng khớp `match` mà cũng khớp `exclude` thì **bỏ qua**. |
| `aggregate` | không | `{ window_s, threshold, group_by }`: chỉ báo khi trúng ≥ `threshold` lần trong `window_s` giây. Không có = trúng là báo. |
| `cooldown_s` | không | Sau khi báo, im lặng bấy nhiêu giây (theo từng giá trị `group_by`). |
| `disabled` | không | `true`: giữ rule trong file nhưng không gửi xuống agent. |
| `tests` | nên có | `match:` / `no_match:` là các dòng log mẫu, chỉ dùng khi kiểm, không gửi xuống agent. |

## 5. Chọn nguồn log: file hay journald

Ubuntu ghi log ở hai nơi. Cùng một sự việc có thể có ở cả hai, nhưng **chỉ nên chọn một** cho mỗi rule để không báo trùng.

| Nguồn | Khi nào dùng | Lấy log mẫu trên máy agent |
|---|---|---|
| `/var/log/auth.log` | đăng nhập SSH, sudo, su, PAM | `sudo tail -n 2000 /var/log/auth.log > mau-auth.log` |
| `/var/log/syslog` | log chung (cron, dịch vụ cũ ghi qua rsyslog) | `sudo tail -n 5000 /var/log/syslog > mau-syslog.log` |
| `/var/log/<ứng dụng>/...` | ứng dụng tự ghi file riêng (ví dụ phần mềm bán hàng) | `sudo tail -n 5000 /var/log/app/app.log > mau-app.log` |
| `journald` + `unit: kernel` | lỗi phần cứng: ổ đĩa, USB, RAM (OOM), mạng | `journalctl -k -o cat --since -7d > mau-kernel.log` |
| `journald` + `unit: <dịch vụ>.service` | log của chính một dịch vụ systemd | `journalctl _SYSTEMD_UNIT=pos.service -o cat --since -7d > mau-pos.log` |
| `journald` + `unit: init.scope` | thông báo của chính systemd về dịch vụ: dừng do lỗi, khởi động lại... | `journalctl _SYSTEMD_UNIT=init.scope -o cat --since -7d > mau-systemd.log` |

Lưu ý:

- **`-o cat` quan trọng**: agent khớp rule trên *nội dung* dòng journal, không có phần ngày giờ / tên máy / tên tiến trình ở đầu. Lấy mẫu bằng `-o cat` thì mẫu giống hệt thứ agent thấy.
- Ngược lại, dòng trong **file** (`/var/log/auth.log`...) có đủ phần đầu `Nov 10 08:01:02 tên-máy sshd[2211]: ...`. Regex nên khớp phần sau tên máy (đừng khớp tên máy hay ngày giờ).
- `unit` so với tiến trình **ghi** dòng log (trường `_SYSTEMD_UNIT`). Câu "cups.service: Failed with result 'exit-code'" do *systemd* ghi, nên unit là `init.scope`, không phải `cups.service`.
- Không biết dòng đó nằm ở unit nào? Trên máy agent chạy:
  `journalctl -o verbose --since -1h | grep -B30 'chuỗi cần tìm' | grep -E '_SYSTEMD_UNIT|_TRANSPORT|MESSAGE='`
- Agent chỉ được đọc file trong `/var/log/`. Đường dẫn có `..`, dấu cách hay ký tự lạ đều bị từ chối.

## 6. Viết regex cho match / exclude

Regex dùng cú pháp **RE2** (của Go). RE2 chạy tuyến tính theo độ dài dòng, nên không có regex nào làm treo agent. Đổi lại, không có lookahead `(?=...)`, lookbehind `(?<=...)` và backreference `\1`: dùng `exclude` để thay cho "khớp A nhưng không có B".

Cú pháp hay dùng:

| Viết | Nghĩa |
|---|---|
| `abc` | chứa chuỗi `abc` ở bất kỳ đâu trong dòng |
| `^abc` / `abc$` | dòng bắt đầu / kết thúc bằng `abc` |
| `.` | một ký tự bất kỳ; muốn dấu chấm thật thì `\.` |
| `\d` `\s` `\S` `\w` | chữ số / khoảng trắng / không phải khoảng trắng / chữ-số-gạch dưới |
| `\d+` `\S+` | một hoặc nhiều |
| `x?` `x*` `x{2,5}` | không hoặc một / không hoặc nhiều / từ 2 tới 5 lần |
| `[0-9a-f]` `[^)]` | một ký tự trong tập / không thuộc tập |
| `(?:A\|B)` | A hoặc B (nhóm không lấy giá trị) |
| `(?P<ip>\S+)` | nhóm có tên `ip`: giá trị lấy ra thành trường `ip` của sự kiện |
| `(?i)error` | không phân biệt hoa thường |
| `\[ \] \( \) \.` | ký tự đặc biệt dạng thường (phải thêm `\`) |

Kinh nghiệm:

- **Cụ thể nhất có thể.** `Failed` khớp hàng trăm loại dòng; `sshd\[\d+\]: Failed password for` chỉ khớp đúng một loại.
- **Neo vào phần cố định của thông điệp** (tên chương trình, cụm từ cố định), đừng neo vào ngày giờ hay tên máy vì chúng đổi theo máy.
- **Không dùng `.*` ở đầu hay cuối**: regex mặc định đã tìm ở bất kỳ đâu trong dòng. `.*` ở giữa thì được nhưng nên thay bằng thứ cụ thể hơn (`\S+`, `[^)]+`).
- **Nhóm có tên** chỉ đặt cho giá trị cần xem trên web hoặc cần đếm riêng (`ip`, `user`, `dev`, `svc`). Tên viết thường, số, `_`, tối đa 8 nhóm.
- Regex khớp được cả dòng trống (ví dụ `.*` hay `a*`) bị **từ chối**, vì nó sẽ khớp mọi dòng.
- Dòng dài hơn 4096 byte bị cắt trước khi khớp.
- Dùng `exclude` để loại trường hợp quen thuộc: `exclude: 'dev (?:loop|sr)\d'` bỏ lỗi của ổ ảo / ổ CD.

## 7. Đếm ngưỡng: aggregate, group_by, cooldown_s

Không có `aggregate`, rule báo **mỗi lần** trúng. Thế là phù hợp với sự việc hiếm và nghiêm trọng (RAID hỏng, đăng nhập root). Với sự việc có thể lặp nhiều (sai mật khẩu, lỗi I/O), dùng `aggregate` để chỉ báo khi có dấu hiệu thật.

```yaml
aggregate: { window_s: 300, threshold: 5, group_by: ip }
cooldown_s: 1800
```

Nghĩa: *cùng một IP* trúng **5 lần trong vòng 300 giây** thì báo **một** sự kiện (`count: 5`). Sau khi báo, IP đó **im lặng 1800 giây** dù còn trúng tiếp. IP khác được đếm riêng.

Diễn biến theo thời gian (threshold 3, window 300 s, cooldown 600 s, cùng một IP):

```
giây:    0     10    30          40 .. 620        700   710   720
trúng:   x     x     x           x x x x          x     x     x
         1     2     3 → BÁO     (đang im lặng,   1     2     3 → BÁO lại
                                  không đếm)
```

- **Cửa sổ trượt**: chỉ tính các lần trúng trong `window_s` giây gần nhất. Ba lần cách nhau 400 giây thì không bao giờ đủ ngưỡng.
- Sau khi báo, bộ đếm của khóa đó về 0.
- `group_by` phải là tên một nhóm có tên trong `match`. Bỏ `group_by` thì đếm chung mọi dòng trúng của rule.
- `cooldown_s` dùng được cả khi không có `aggregate`: ví dụ USB cắm vào sinh vài dòng một lúc, `cooldown_s: 60` để chỉ báo một lần.
- Agent giữ tối đa 4096 khóa nhóm cho mỗi rule (ví dụ 4096 IP khác nhau đang đếm). Khóa lâu không gặp tự được xóa.

Gợi ý con số:

| Loại | Gợi ý |
|---|---|
| Dò mật khẩu (SSH, web) | `window_s: 300, threshold: 5, group_by: ip`, `cooldown_s: 1800` |
| Lỗi phần cứng lặp lại | `window_s: 600, threshold: 3`, `cooldown_s: 3600` |
| Sự việc hiếm, nghiêm trọng | không aggregate, không cooldown |
| Sự việc sinh nhiều dòng một lúc | không aggregate, `cooldown_s: 60` |

## 8. Chọn severity

| Mức | Dùng cho | Ví dụ |
|---|---|---|
| `critical` | mất dữ liệu / dừng bán hàng nếu không xử lý ngay | RAID hỏng, ổ sắp chết, phần mềm bán hàng chết hẳn |
| `high` | cần xử lý trong ngày | lỗi I/O, dịch vụ quan trọng dừng do lỗi, OOM, đăng nhập root |
| `medium` | đáng ngờ, cần xem | dò mật khẩu SSH |
| `low` | ghi nhận, xem khi rảnh | cắm USB, sudo sai mật khẩu |
| `info` | chỉ để thống kê / điều tra | cron chạy lệnh |

Đừng để quá nhiều rule `high` / `critical`: cảnh báo nhiều quá thì người trực sẽ bỏ qua cả cảnh báo thật.

## 9. Viết tests cho rule

Mỗi rule nên có ít nhất **một dòng phải khớp và một dòng không được khớp**, đều là dòng log **thật** lấy từ máy, giữ nguyên định dạng.

**Che thông tin thật trước khi dán vào tests.** Dòng log thật thường có tên máy, tên tài khoản, IP, tên miền của hệ thống. File rule được lưu lại, chép đi nhiều nơi (và bộ mẫu trong thư mục code được đưa lên GitHub), nên thay bằng giá trị chung, giữ nguyên định dạng:

| Thông tin thật | Thay bằng |
|---|---|
| tên tài khoản (`Accepted password for <tên>`, `/home/<tên>`) | `user`, `/home/user` |
| tên máy ở đầu dòng | theo quy ước `12345-TN1-PRI` |
| IP máy thật | `10.0.5.20`, `192.168.1.10` (hoặc IP trong dải tài liệu `192.0.2.x`) |
| tên miền | `ten.ddns.net`, `example.com` |
| mật khẩu, khóa, mã khách hàng | xóa hẳn khỏi dòng |

Regex không phụ thuộc vào các giá trị này (xem [mục 6](#6-viết-regex-cho-match--exclude)), nên đổi xong tests vẫn đúng.

```yaml
    tests:
      match:
        - 'blk_update_request: I/O error, dev sda, sector 123456 op 0x0:(READ) flags 0x0'
      no_match:
        - 'blk_update_request: I/O error, dev loop3, sector 8 op 0x0:(READ)'
```

- `match`: mỗi dòng phải khớp `match` và **không** bị `exclude` loại.
- `no_match`: mỗi dòng không được khớp (hoặc bị `exclude` loại). Chọn dòng **gần giống** dòng thật nhất: đó là chỗ regex hay sai.
- Tests chỉ kiểm điều kiện khớp, không tính `aggregate` (muốn thử ngưỡng thì dùng `rules test` với `-step`, [mục 10](#10-các-lệnh)).
- `rules check` và `rules build` đều chạy tests. Test không đạt thì **không ký được**.
- Test tự động `server/internal/rulefile` kiểm mọi file trong `server/rules/*.yaml`, nên `go test` cũng bắt được rule mẫu bị hỏng.

## 10. Các lệnh

### Trên server

```bash
# Kiểm cú pháp, giới hạn và tests (nhiều file một lúc được)
scs-server rules check server/rules/mau-co-ban.yaml

# Chạy cả bộ rule trên file log mẫu, in sự kiện sẽ báo
scs-server rules test server/rules/mau-co-ban.yaml -log mau-auth.log
#   -source NGUON   dòng trong file coi như đến từ nguồn nào (mặc định: nguồn của rule đầu tiên)
#   -unit UNIT      với journald
#   -step 1s        khoảng thời gian giả lập giữa hai dòng (thử aggregate)

# Ví dụ thử rule kernel với log lấy bằng journalctl -k -o cat
scs-server rules test server/rules/mau-co-ban.yaml -log mau-kernel.log -source journald -unit kernel

# Thử ngưỡng: coi các dòng cách nhau 2 phút (5 lần trong 300 s sẽ không đủ)
scs-server rules test server/rules/mau-co-ban.yaml -log mau-auth.log -step 2m

# Kiểm rồi ký bằng khóa phát hành, ghi bộ rule đã ký
scs-server rules build server/rules/mau-co-ban.yaml -key /etc/scs-server/pki/release.key -o mau-co-ban.json
```

Đường dẫn khóa phát hành đúng là giá trị `SCS_RELEASE_KEY` trong `/etc/scs-server/server.env` (đọc khóa cần quyền của user dịch vụ, chạy bằng `sudo -u scs`). Chạy `rules check` / `rules test` không cần khóa.

### Trên server Ubuntu đã cài (đường dẫn thật)

Lệnh `scs-server` không nằm trong PATH; binary đã cài ở `/opt/scs-server/scs-server`. Các lệnh trên viết đầy đủ:

```bash
S=/opt/scs-server/scs-server

# Kiểm và chạy thử: user thường, không cần sudo
$S rules check ~/scs-rules/cong-ty.yaml
$S rules test  ~/scs-rules/cong-ty.yaml -log ~/mau-auth.log

# Ký: khóa phát hành chỉ user scs (và root) đọc được, nên chạy bằng sudo
sudo $S rules build ~/scs-rules/cong-ty.yaml -key /etc/scs-server/pki/release.key -o ~/cong-ty.json
sudo chown "$USER": ~/cong-ty.json
```

- Không chạy `sudo -u scs ... ~/scs-rules/...`: user `scs` không vào được thư mục nhà của bạn (Ubuntu để quyền 750), sẽ báo `permission denied`.
- File `.json` đã ký **không bí mật** (chỉ có rule và chữ ký); chép đi đâu cũng được. Khóa `release.key` thì **không bao giờ** chép ra khỏi server.
- Sau khi cập nhật server, binary mới cũng ở đúng chỗ đó; lệnh rule đổi theo bản server.

Chép bộ rule đã ký sang máy agent (từ server, hoặc bất kỳ máy nào vào được cả hai):

```bash
scp ~/cong-ty.json user@<IP máy agent>:/tmp/
```

Kết quả `rules test` cho thấy dòng nào sinh sự kiện và sự kiện gửi lên server trông thế nào:

```
dòng 5 → SSH-FAIL-01 [medium] count=5
  {"rule_id":"SSH-FAIL-01","severity":"medium","source":"/var/log/auth.log","first":"...","last":"...","count":5,
   "sample":"Nov 10 08:05:00 h sshd[225]: Failed password for root from 10.0.5.20 port 5 ssh2","fields":{"ip":"10.0.5.20","user":"root"}}

Đã đọc 7 dòng (nguồn /var/log/auth.log), 2 sự kiện.
```

### Trên máy agent

```bash
# Bộ rule đang dùng: phiên bản, các rule, nguồn log agent sẽ đọc
sudo scs-agent rules

# Cài tay một bộ rule đã ký (chép file .json từ server sang)
sudo scs-agent rules -install mau-co-ban.json

# Chạy bộ rule đang dùng trên log thật của chính máy này
sudo scs-agent rules -log /var/log/auth.log -source /var/log/auth.log
journalctl -k -o cat --since today | sudo scs-agent rules -log - -source journald -unit kernel
```

`-install` kiểm chữ ký bằng `/opt/scs-agent/release.pub` (có sẵn trong gói cài). File ký bằng khóa khác hoặc bị sửa thì bị từ chối, bộ đang dùng giữ nguyên.

- Bộ rule đang dùng lưu ở `/var/lib/scs-agent/rules/current.json`. Cài bằng `sudo` thì file vẫn thuộc user `scs-agent` (chủ của `/var/lib/scs-agent`), nên dịch vụ agent đọc được. **Đừng** tự chép file vào đó bằng `cp`: dùng `-install` để có kiểm chữ ký và đúng quyền.
- Cài xong xóa file tạm: `rm /tmp/cong-ty.json`.
- Gỡ bộ rule (về trạng thái chưa có rule): `sudo rm /var/lib/scs-agent/rules/current.json`.

## 11. Ví dụ làm từ đầu

Bài toán: phần mềm bán hàng chạy dưới dạng dịch vụ `pos.service`, khi mất kết nối CSDL thì ghi dòng `DB connection lost`. Cần báo khi mất kết nối liên tục (không báo khi chập chờn một lần), nhưng bỏ qua lúc đang khởi động lại có chủ ý (có chữ `shutdown`).

**Bước 1: lấy mẫu trên máy agent**

```bash
journalctl _SYSTEMD_UNIT=pos.service -o cat --since -7d > mau-pos.log
grep -n 'DB connection' mau-pos.log | head
```

Giả sử thấy:

```
2026-11-09 10:12:01 ERROR [db] DB connection lost: dial tcp 10.0.0.5:5432: connect: connection refused (retry 1)
2026-11-09 10:12:06 ERROR [db] DB connection lost: dial tcp 10.0.0.5:5432: i/o timeout (retry 2)
2026-11-09 22:00:00 WARN  [db] DB connection lost during shutdown
2026-11-09 10:12:11 INFO  [db] DB connection restored
```

**Bước 2 + 3: viết rule và tests** (thêm vào cuối `rules:` trong `~/scs-rules/cong-ty.yaml`)

```yaml
  - id: POS-DB-LOST-01
    title: Phần mềm bán hàng mất kết nối CSDL liên tục
    severity: critical
    source: journald
    unit: pos.service
    match: 'ERROR \[db\] DB connection lost: (?P<reason>.+?) \(retry \d+\)'
    exclude: 'during shutdown'
    aggregate: { window_s: 120, threshold: 6 }   # khoảng 6 lần trong 2 phút (thử lại mỗi 5 s)
    cooldown_s: 900
    tests:
      match:
        - '2026-11-09 10:12:01 ERROR [db] DB connection lost: dial tcp 10.0.0.5:5432: connect: connection refused (retry 1)'
      no_match:
        - '2026-11-09 22:00:00 WARN  [db] DB connection lost during shutdown'
        - '2026-11-09 10:12:11 INFO  [db] DB connection restored'
```

**Bước 4: kiểm**

```bash
/opt/scs-server/scs-server rules check ~/scs-rules/cong-ty.yaml
```

**Bước 5: chạy thử trên mẫu** (các dòng trong journal cách nhau khoảng 5 s)

```bash
/opt/scs-server/scs-server rules test ~/scs-rules/cong-ty.yaml -log mau-pos.log -source journald -unit pos.service -step 5s
```

Không báo lần nào dù trong mẫu có lúc mất kết nối thật? Hạ `threshold`. Báo cả chục lần một ngày lúc mạng chập chờn? Tăng `threshold` hoặc `window_s`.

**Bước 6: tăng `version`, ký, cài**

```bash
sudo /opt/scs-server/scs-server rules build ~/scs-rules/cong-ty.yaml -key /etc/scs-server/pki/release.key -o ~/cong-ty.json
scp ~/cong-ty.json user@<IP máy agent>:/tmp/
# trên máy agent:
sudo scs-agent rules -install /tmp/cong-ty.json
sudo scs-agent rules          # phải thấy POS-DB-LOST-01 và nguồn "journald unit: ... pos.service"
```

## 12. Lỗi hay gặp

| Thông báo / hiện tượng | Nguyên nhân | Sửa |
|---|---|---|
| `unknown field "serverity"` | gõ sai tên trường | sửa đúng tên ([mục 4](#4-từng-trường-của-một-rule)) |
| `cannot unmarshal number into ... version` | `version` không để trong nháy | `version: '2026.11.10-01'` |
| `YAML sai: ... found a tab character that violates indentation` | thụt lề bằng tab | thụt lề bằng dấu cách |
| `YAML sai: ...` khác (ví dụ `did not find expected key`) | thụt lề lệch, hoặc regex có `: ` `#` `{` mà không để trong nháy | thẳng hàng các trường của một rule, regex luôn trong `'...'` |
| `regex sai: missing closing )` | thiếu `)`, hoặc `(` `[` thật mà chưa thêm `\` | `\(` `\[` |
| `regex khớp cả dòng trống` | regex kiểu `.*`, `a*`, `(x)?` | viết phần bắt buộc phải có |
| `invalid or unsupported Perl syntax: (?=` / `(?<` | RE2 không có lookahead / lookbehind | tách phần "không có" ra `exclude` |
| `group_by "ip": match không có nhóm` | `group_by` không trùng tên nhóm trong `match` | thêm `(?P<ip>...)` hoặc sửa tên |
| `source ... phải là đường dẫn file tuyệt đối trong /var/log` | file ngoài `/var/log`, đường dẫn tương đối hoặc có `..` | chỉ đọc log trong `/var/log` |
| `unit: chỉ dùng khi source là journald` | đặt `unit` cho rule đọc file | bỏ `unit` hoặc đổi `source: journald` |
| `id bị trùng` | hai rule cùng `id` | đổi một rule sang số khác (`-02`) |
| `rules test` báo `Không có rule nào đọc nguồn ...` | `-source` / `-unit` không khớp rule nào | đặt đúng `-source` (file hay `journald`) và `-unit` |
| Tests đạt nhưng trên máy không bao giờ báo | mẫu lấy từ file nhưng rule đọc journald (hoặc ngược lại), sai unit | lấy mẫu đúng nguồn ([mục 5](#5-chọn-nguồn-log-file-hay-journald)), thử bằng `scs-agent rules -log` trên máy |
| Báo quá nhiều | regex rộng, thiếu aggregate / cooldown | regex cụ thể hơn, thêm `exclude`, `aggregate`, `cooldown_s` |
| Menu mục 4 / `update.sh` báo `Thư mục code có thay đổi chưa commit` | đã sửa file rule mẫu trong `~/SCS-System/server/rules/` | chép file sang `~/scs-rules/`, rồi trả file mẫu về như cũ: `cd ~/SCS-System && git checkout -- server/rules/` |
| `rules build` báo `permission denied` với `release.key` | chạy bằng user thường | `sudo /opt/scs-server/scs-server rules build ...` ([mục 10](#trên-server-ubuntu-đã-cài-đường-dẫn-thật)) |
| `rules build` bằng `sudo -u scs` báo `permission denied` với file `.yaml` | user `scs` không vào được thư mục nhà | dùng `sudo` (root) thay cho `sudo -u scs` |
| `scs-server: command not found` | binary không nằm trong PATH | gọi `/opt/scs-server/scs-server` |
| Chạy trong Git Bash trên Windows, `-source /var/log/...` không ra gì | Git Bash tự đổi `/var/log/...` thành đường dẫn Windows | chạy trên Ubuntu, hoặc đặt `MSYS_NO_PATHCONV=1` trước lệnh |

## 13. Giới hạn

| Giới hạn | Giá trị |
|---|---|
| Số rule trong một bộ | 500 |
| Độ dài regex | 1024 ký tự |
| Nhóm có tên trong `match` | 8 |
| `window_s`, `cooldown_s` | tối đa 86400 (1 ngày) |
| `threshold` | 1 – 10000 |
| Dòng log được khớp | 4096 byte đầu |
| Dòng mẫu gửi kèm sự kiện (`sample`) | 1024 byte |
| Giá trị mỗi trường lấy ra | 256 byte |
| Khóa nhóm đếm cùng lúc mỗi rule | 4096 |
| Dòng tests | 20 mỗi loại |
| Bộ rule đã ký | 1 MB |

## 14. An toàn

- Rule **chỉ đọc log và khớp regex**. Không có trường nào để chạy lệnh, gọi mạng hay đọc file ngoài `/var/log`. Bộ rule dù bị sửa cũng không làm được gì trên máy ngoài việc báo sai.
- Agent chỉ nhận bộ rule **có chữ ký** của khóa phát hành server (cùng khóa ký bản agent, nhưng khác "ngữ cảnh ký" nên chữ ký bộ rule không dùng thay được cho chữ ký bản agent và ngược lại). Agent kiểm lại toàn bộ nội dung y như server, sai một chỗ là bỏ cả bộ.
- Regex RE2 không làm treo agent dù viết kém. Các giới hạn ở [mục 13](#13-giới-hạn) giữ RAM / CPU agent thấp.
- Sự kiện gửi lên server kèm dòng log khớp (`sample`). Đừng viết rule khớp những dòng chứa mật khẩu hay dữ liệu khách hàng. Nếu phải theo dõi loại dòng đó, đặt nhóm có tên để lấy riêng phần cần (ví dụ mã lỗi) và cân nhắc kỹ.

## 15. Hiện trạng: đã có / chưa có

| Phần | Trạng thái |
|---|---|
| Định nghĩa rule, kiểm tra, bộ máy khớp + đếm ngưỡng + cooldown (dùng chung) | **Đã có** (`common/rules`, có test) |
| Đọc YAML, `scs-server rules check / test / build`, ký bộ rule | **Đã có** |
| Agent nạp / cài bộ rule có chữ ký, `scs-agent rules` (xem, cài tay, chạy thử) | **Đã có** |
| Agent đọc file log liên tục (nhớ vị trí đã đọc, xử lý xoay vòng log) và đọc journald | Chưa có (mốc A3) |
| Server lưu bộ rule, gán bộ rule theo nhóm máy trên web, phát qua heartbeat (`rule_set_version`) | Chưa có (mốc M7) |
| Agent gửi sự kiện lên server, hàng đợi khi mất mạng; server lưu sự kiện, tạo cảnh báo trên web | Chưa có (mốc A3 / M5) |

Trong lúc chưa có các phần trên, quy trình là: viết rule trong `~/scs-rules/` → `rules check` / `rules test` trên server → `sudo ... rules build` → chép file `.json` sang máy → `scs-agent rules -install` → `scs-agent rules -log ...` để xem rule bắt được gì trên log thật của máy đó.

Lưu ý: dịch vụ agent **chưa** tự đọc log theo bộ rule (mốc A3). Hiện bộ rule chỉ dùng được bằng lệnh `scs-agent rules -log` để chạy thử; cài sẵn bây giờ thì khi có A3 agent dùng luôn.

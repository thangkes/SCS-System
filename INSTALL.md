# Cài đặt SCS-System

Hai phần cài riêng, trên hai loại máy khác nhau:

| | Máy | Script | Làm một lần hay nhiều lần |
|---|---|---|---|
| **SCS-Server** | máy server trung tâm (Ubuntu ≥ 22.04) | `bash server/menu.sh` | một lần |
| **SCS-Agent** | từng máy Ubuntu tại điểm bán | `sudo bash agent/menu.sh` | mỗi máy một lần |

Cách dùng chính là **menu**: chọn số, script hỏi những gì cần (IP, dải mạng, file CA) và gợi ý sẵn giá trị.
Các script bên dưới menu (`scripts/*.sh`) vẫn dùng bằng cờ lệnh được khi cần tự động hóa hoặc cài hàng loạt;
phần "Dùng cờ lệnh" ở từng mục bên dưới ghi lệnh tương đương.

Thứ tự: **cài server trước**. Cài server xong mới có file `agent-ca.pem` (CA của server) để mang sang các máy agent.

Cả hai script đều chạy lại nhiều lần được: phần nào đã có thì giữ nguyên, chỉ cập nhật binary và khởi động lại dịch vụ.

---

## 1. SCS-Server

```bash
git clone https://github.com/thangkes/SCS-System.git
bash SCS-System/server/menu.sh
```

Chạy bằng user thường hoặc root (thư mục code thuộc user nào thì script tự chạy bằng user đó). Lần đầu chọn lần lượt **1 → 2 → 3**:
**1** kiểm tra điều kiện, **2** cài Docker/Go/PostgreSQL/NATS, **3** cài dịch vụ (hỏi IP LAN, tùy chọn IP WAN /
tên miền DDNS, dải mạng được vào web; gợi ý sẵn IP LAN và dải LAN của máy). Về sau: **4** cập nhật, **6** cho thêm dải mạng vào web,
**8** lấy mã đăng nhập web, **9** xem trạng thái và log, **17** điền Gmail gửi mã để bật tài khoản web đăng nhập bằng email.

### 1.1 Chuẩn bị máy: Docker, Go, PostgreSQL + NATS (dùng cờ lệnh)

```bash
git clone https://github.com/thangkes/SCS-System.git
cd SCS-System/server
bash scripts/setup.sh --check          # chỉ quét điều kiện, không cài gì
bash scripts/setup.sh --start --test   # cài Docker + Go, tạo .env (sinh mật khẩu), chạy Postgres + NATS, chạy test
```

`setup.sh` chạy bằng user thường (tự gọi sudo khi cần), ghi log ở `server/setup.log`.

### 1.2 Cài dịch vụ scs-server

```bash
sudo bash scripts/install.sh --wan-ip <IP WAN của server> --vpn-ip <IP VPN để vào web>
```

Script:
- tạo user hệ thống `scs`, chép binary vào `/opt/scs-server`, cấu hình vào `/etc/scs-server/server.env`;
- tạo **CA nội bộ** và **chứng chỉ server** ghi các IP đã cho (vào `/etc/scs-server/pki`, khóa CA quyền 600)
  và tự điền `SCS_TLS_*`, `SCS_CA_*`;
- cài và khởi động `scs-server.service` (tự chạy khi khởi động máy, chết thì tự bật lại);
- chép bản CA cho agent ra `/etc/scs-server/agent-ca.pem` (chỉ là chứng chỉ, không bí mật);
- cài lệnh `scs-login-code` để lấy mã đăng nhập web (mã **Admin** = quản trị gốc, mã **Chỉ xem**).

### 1.3 Mạng và vào web từ máy khác trong LAN

- **Chỉ mạng nội bộ** (mặc định): chứng chỉ server chỉ ghi IP LAN, agent gọi `https://<IP LAN>:6222`. Đặt IP tĩnh cho
  server (hoặc giữ IP trên router). ufw đang bật thì script cài tự mở cổng 6222 (cổng agent được bảo vệ bằng TLS,
  chứng chỉ máy và admin duyệt, không dựa vào tường lửa).
- **Cổng agent 6222 không phụ thuộc IP trong chứng chỉ:** agent chỉ kiểm chứng chỉ server do CA của server cấp (CA
  đóng sẵn trong gói), không so IP / tên miền. Agent ở VLAN khác, gọi qua IP WAN có NAT, hay server đổi IP đều không
  phải cấp lại chứng chỉ. Kẻ khác không giả làm server được vì không có chứng chỉ do CA này cấp cho server (chứng
  chỉ của các máy agent chỉ dùng được cho client).
- **Mở cho agent từ Internet** (làm lúc cài hoặc sau này): router **chuyển tiếp một cổng** (ví dụ `16222`) về `6222` của
  máy server, rồi menu mục **7** thêm IP WAN tĩnh hoặc tên miền DDNS để agent đã duyệt nhận làm địa chỉ dự phòng.
  Máy ở ngoài cài bằng gói đóng với `https://<IP WAN hoặc tên miền>:16222`.
- **Agent tự chuyển địa chỉ:** server giữ danh sách địa chỉ server (IP LAN, IP WAN, tên miền DDNS, IP VPN; tối đa 8)
  trong file `SCS_AGENT_SERVERS_FILE` (mặc định `/var/lib/scs-server/data/agent-servers.json`) và gửi cho agent đã
  duyệt (lúc duyệt và mỗi heartbeat, 5–30 phút một lần); agent lưu lại, địa chỉ đang dùng không kết nối được thì thử ngay địa
  chỉ kế tiếp, khởi động lại thì thử địa chỉ gọi được lần cuối trước. Máy cài bằng gói LAN mang ra ngoài vẫn gọi về
  được qua IP WAN. Mục 3/7 ghi cả danh sách; thêm / gỡ từng địa chỉ: web trang **Địa chỉ server** hoặc menu
  mục **15** (`scs-server servers list|add|remove|set`), có hiệu lực ngay, không khởi động lại, không cấp lại chứng chỉ.
- **Đổi địa điểm / đổi mạng không mất kết nối hàng loạt:**
  1. **Trước khi chuyển**, lúc agent còn gọi được server: thêm địa chỉ mới (IP WAN mới, tên miền DDNS) và mở
     chuyển tiếp cổng trên router mới.
  2. Chờ trang **Địa chỉ server** báo "Đã nhận danh sách hiện tại" gần 100% (mỗi máy nhận ở heartbeat kế tiếp).
  3. Chuyển server sang mạng mới. Agent thấy địa chỉ cũ không gọi được thì tự chuyển sang địa chỉ kế tiếp.
  4. Khi cột "Máy đang dùng" của địa chỉ cũ về 0, gỡ địa chỉ cũ (web hỏi lại nếu vẫn còn máy dùng).
  Không biết trước IP mới thì không báo trước cho agent được: nên luôn có ít nhất một tên miền DDNS (hoặc IP VPN)
  trong danh sách. Số liệu trên trang tính trong RAM từ lúc server khởi động (chạy `scs-server all`); agent bản cũ
  vẫn nhận danh sách nhưng không báo đã nhận (đếm ở ô "Agent bản cũ").
- Cổng web quản trị `7222` mặc định **không mở ra Internet**: chỉ chính máy server vào được. Menu mục **6** (hoặc
  câu hỏi ở mục 3) chọn: **toàn mạng nội bộ** (mọi dải IP riêng 10.x, 172.16–31.x, 192.168.x, hợp với công ty nhiều
  lớp mạng / VLAN định tuyến qua nhau), chỉ dải LAN của server, tự nhập các dải, hoặc **5) cả Internet** (phải gõ
  `INTERNET` để xác nhận). Dòng lệnh: `sudo bash server/scripts/install.sh --admin-allow private` (hoặc
  `--admin-allow 10.10.0.0/16`, lặp lại được; `--admin-allow internet` = mọi IP, gõ thẳng `0.0.0.0/0` bị từ chối).
  Script thêm vào `SCS_ADMIN_ALLOW` và mở ufw cho đúng các dải đó nếu ufw đang bật.
  **Mở cả Internet:** web chỉ được bảo vệ bằng mã 6 số (mã SSH, hoặc mã gửi về Gmail nếu bật tài khoản email, mục 1.5),
  người lạ có thể gửi mã SSH sai để khóa đăng nhập bằng mã SSH (mở khóa trên server: `sudo scs-login-code -unlock`), chứng chỉ tự ký; router phải chuyển tiếp cổng 7222. An toàn hơn: VPN
  (WireGuard, Tailscale) rồi chỉ cho dải VPN. Vào từ trong cùng mạng với server thì dùng IP LAN (nhiều modem không
  cho gọi IP WAN của chính mình từ bên trong).
- Riêng web quản trị 7222: muốn trình duyệt không báo sai tên, chứng chỉ server phải ghi IP bạn gõ trên trình duyệt (IP LAN, IP VPN):
  menu mục **7** cấp lại chứng chỉ với đủ các IP (CA giữ nguyên, agent không bị ảnh hưởng). Trình duyệt vẫn cảnh báo
  "chưa tin cậy" vì CA là CA nội bộ: nhập file `/etc/scs-server/agent-ca.pem` vào kho chứng chỉ tin cậy của máy
  admin để hết cảnh báo.

### 1.4 Giai đoạn, cảnh báo và mất liên lạc

Server lưu mỗi máy theo **giai đoạn** OK / cảnh báo / mất liên lạc ([thiết kế](docs/thiet-ke-luu-theo-giai-doan.md)).
Hai hạn chỉnh trong `/etc/scs-server/server.env` (không có dòng thì dùng mặc định), sửa xong `sudo systemctl restart scs-server`:

```bash
SCS_ALERT_AUTOCLOSE=24h   # cảnh báo mức high tự đóng sau bấy lâu không có sự kiện high / critical mới (critical: chỉ admin)
SCS_OFFLINE_AFTER=30m     # không nghe máy gửi gì bấy lâu thì báo mất liên lạc (tự đóng khi máy gửi lại); nên từ 20m
```

Xem dòng thời gian của máy: web, trang **Máy** → bấm mã siêu thị (viền đỏ / vàng / xanh theo sự cố) để xổ danh sách → bấm vào máy. Đóng sự cố: trang **Cảnh báo** → **Đã xử lý**.

Thông tin siêu thị (tên, tổ chức, IP, người phụ trách, ghi chú): trang **Máy** → bấm mã siêu thị → **Sửa thông tin siêu
thị** (mức 6), hoặc nạp hàng loạt bằng **Nạp thông tin từ file** (.xlsx / .csv, có ghép cột, chế độ Bổ sung / Ghi đè, xem
trước; mức 2 trở lên). Báo cáo vận hành server (CPU, RAM, ổ đĩa, băng thông, heartbeat theo giờ / ngày, xuất Excel / CSV,
in PDF): trang **Báo cáo server**; giữ số liệu `SCS_METRICS_KEEP` (mặc định 90 ngày, ví dụ `SCS_METRICS_KEEP=4320h` = 180
ngày).

### 1.5 Sau khi cài

```bash
scs-login-code                     # lấy mã 6 số để đăng nhập web https://<IP VPN>:7222
journalctl -u scs-server -f        # xem log
sudo bash scripts/install.sh --uninstall   # gỡ dịch vụ (giữ cấu hình, CA, dữ liệu)
```

Bật đăng nhập bằng tài khoản email (tùy chọn, xem `server/README.md` mục "Tài khoản và mức quyền"): tạo một Gmail
riêng để gửi mã, bật xác minh 2 bước, tạo **Mật khẩu ứng dụng** (16 chữ cái), rồi chạy menu **mục 17** → `s`: nhập
Gmail gửi mã, dán mật khẩu ứng dụng (không hiện khi gõ, không nằm trên dòng lệnh), tên miền được đăng ký (mặc định
`gmail.com`), gửi thư thử tới Gmail của bạn; menu ghi vào `/etc/scs-server/server.env` (sao lưu bản cũ) và khởi động lại
dịch vụ. Mục 17 còn có `t` gửi thư thử, `k` tắt đăng nhập bằng email.

Server cài từ bản cũ: cập nhật (mục 4) tự thêm khối `SCS_SMTP_*` (để trống = tắt) vào cuối `server.env`. Sửa tay thì
điền `SCS_SMTP_HOST=smtp.gmail.com`, `SCS_SMTP_USER=<gmail-gui-ma>@gmail.com`, `SCS_SMTP_PASSWORD=<mat-khau-ung-dung>`,
rồi `sudo systemctl restart scs-server`; thử gửi bằng
`sudo -u scs /opt/scs-server/scs-server mail-test -to <gmail-cua-ban> -env-file /etc/scs-server/server.env` (chạy từ thư mục `/`). Trang đăng nhập sẽ có thêm tab **Email** và **Đăng ký**; tài khoản mới phải
được quản trị gốc (đăng nhập bằng `scs-login-code`) duyệt ở trang **Tài khoản**.

### 1.6 Cập nhật server

```bash
cd ~/SCS-System
bash server/scripts/update.sh --check      # xem có bản mới không
bash server/scripts/update.sh              # cập nhật lên tag phát hành mới nhất (chưa có tag thì lên main)
bash server/scripts/update.sh --ref v0.2.0 # lên đúng một bản
bash server/scripts/update.sh --rollback   # quay về binary trước lần cập nhật gần nhất
```

Chạy bằng user thường hoặc root (tự gọi sudo khi cài). Script kéo code, chạy test (kể cả test SQL trên DB riêng
`scs_test` của PostgreSQL đang chạy — lỗi thì không cài), build; bản mới có đổi cấu trúc DB thì
**sao lưu DB** vào `server/data/backup/` trước; cài xong kiểm `/readyz`, không lên trong 60 giây thì **tự quay về
binary cũ**. Thư mục code có thay đổi chưa commit thì từ chối chạy. Mỗi lần cập nhật cũng build **bản agent cùng
phiên bản** (amd64, arm64); server nhập vào kho, admin phát hành trên web (mục 2).

**Sao lưu DB định kỳ**: ngày 1 và 16 hằng tháng (≈ 15 ngày), giữ 6 bản gần nhất; menu mục **18** để sao lưu ngay, xem
các bản và cách khôi phục (chi tiết: `server/README.md`, mục "Sao lưu DB"). Nên chép thư mục `server/data/backup/`
ra máy khác định kỳ.

Khi chạy thật nên đặt `SCS_ENV=prod` trong `/etc/scs-server/server.env` (bắt buộc đủ TLS, CA, mật khẩu DB) rồi
`sudo systemctl restart scs-server`.

---

## 2. SCS-Agent (mỗi máy tại điểm bán)

**Cách hoạt động:** máy tại điểm bán chỉ cài một lần **bản agent khung** (đóng gói trên server). Bản khung đăng ký
với server và chờ duyệt (admin duyệt tay, hoặc duyệt tự động nếu đã bật ở trang **Chờ duyệt** và máy đủ điều kiện:
hostname đúng, siêu thị đã có thông tin + IP WAN trong dữ liệu đã nạp, máy gửi từ IP đó). Duyệt xong, agent nhận qua
heartbeat (ngẫu nhiên 5–30 phút một lần):

- **cấu hình** đặt trên web, trang **Agent** (dịch vụ cần theo dõi: chung, theo siêu thị, theo máy);
- **bản agent cần chạy**: mỗi lần cập nhật server (menu mục 4) có thêm bản agent cùng phiên bản; admin bấm
  **Phát hành nhóm thử** (các máy đánh dấu "nhóm thử" trong chi tiết máy) rồi **Phát hành tất cả**. Agent tải bản
  mới, kiểm **chữ ký của khóa phát hành** (khóa công khai đóng trong gói, root sở hữu), rồi tự khởi động lại chạy bản
  đó. Bản mới **chết 3 lần** (trong cùng một lần bật máy) mà chưa gửi được heartbeat thì agent quay về bản khung
  và 24 giờ sau mới thử lại bản đó. Máy tắt / khởi động lại hay mất mạng tới server **không** bị tính là lỗi.
- **bản khung cũng được thay từ server**, không phải cài lại máy: khi một bản agent mới đã chạy tốt trên máy được
  1 giờ, tác vụ `scs-agent-promote.timer` (chạy bằng root, 30 phút một lần) kiểm lại chữ ký rồi chép bản đó vào
  `/opt/scs-agent` làm bản khung, giữ bản khung cũ. Lần khởi động sau, nếu bản khung mới không gọi được server trong
  30 phút thì máy tự trả lại bản khung cũ và không dùng bản đó làm bản khung nữa. Xem tình trạng:
  `systemctl list-timers scs-agent-promote.timer`, `journalctl -u scs-agent-promote -n 20`,
  `cat /opt/scs-agent/bootstrap.json`. Máy cài bằng gói cũ (trước bản này) chưa có tác vụ này: cài lại một lần
  bằng gói mới (không phải duyệt lại).

Không phải đóng gói hay cài lại khi có bản mới hoặc đổi cấu hình.

### 2.1 Tạo gói cài trên máy server (một lần)

Menu server mục **11** (hoặc `bash server/scripts/package-agent.sh --server https://<IP LAN>:6222`). Gói thành
**một file** `server/data/dist/scs-agent-<phiên bản>-linux-amd64.tar.gz` (kèm file `.sha256`), gồm: binary
`scs-agent` (đúng bản server đang giữ), script cài, unit systemd, menu, **CA của server**, **khóa công khai phát hành**
và **địa chỉ server đặt sẵn**. Gói không chứa bí mật (chỉ chứng chỉ và khóa công khai; script từ chối nếu lỡ đưa file
khóa riêng vào).

### 2.2 Cài trên máy tại điểm bán

Đặt hostname đúng quy ước trước (server đọc mã điểm bán và vị trí máy từ đây):
`12345-TN1-PRI` (máy 1), `12345-TN2-SEC` (máy 2), `12345-TN3`, `12345-TN4`… Đổi: `sudo hostnamectl set-hostname 12345-TN1-PRI`.

Chép file `.tar.gz` sang (USB, `scp`), rồi:

```bash
tar xzf scs-agent-*.tar.gz
```
```bash
sudo bash scs-agent/scripts/install.sh
```

Không cần nhập gì: địa chỉ server và CA lấy từ gói. Muốn kiểm tra trước hoặc xem trạng thái sau khi cài:
`sudo bash scs-agent/menu.sh` (**1** kiểm tra, **2** cài, **3** trạng thái, **4** mã khóa máy, **5** log).

### 2.3 Cài không dùng gói (dùng cờ lệnh)

Khi không dùng gói: build binary (`cd agent && CGO_ENABLED=0 GOOS=linux go build -trimpath -o bin/scs-agent ./cmd/scs-agent`),
chép thư mục `agent/` và `/etc/scs-server/agent-ca.pem` sang máy cần cài, rồi chạy `install.sh` với `--server`, `--ca`
như các mục dưới.

### 2.4 Kiểm tra trước (không thay đổi gì trên máy)

```bash
sudo bash agent/scripts/install.sh --check --server https://<IP WAN>:16222 --ca agent-ca.pem
```

Kiểm: Ubuntu, systemd, binary chạy được, hostname đúng quy ước, đồng hồ đồng bộ NTP, **kết nối tới server**
và chứng chỉ server khớp CA.

### 2.5 Cài bằng cờ lệnh

```bash
sudo bash agent/scripts/install.sh --server https://<IP WAN>:16222 --ca agent-ca.pem [--watch pos.service]
```

Script:
- tạo user hệ thống `scs-agent` (thuộc nhóm `adm`, `systemd-journal` để đọc log), không chạy bằng root;
- chép binary vào `/opt/scs-agent` (lệnh `scs-agent` dùng được ở mọi nơi), CA và cấu hình vào `/etc/scs-agent`;
- cài `scs-agent.service`: giới hạn CPU 10%, RAM 128 MB, ưu tiên thấp; chỉ ghi được `/etc/scs-agent` và `/var/lib/scs-agent`;
- khởi động; agent tự sinh khóa riêng của máy và gửi đăng ký;
- in **mã khóa máy**.

`--watch` (tùy chọn): các dịch vụ systemd cần báo trạng thái trong heartbeat, ngăn bằng dấu phẩy.

### 2.6 Duyệt máy

Admin vào web quản trị → trang **Chờ duyệt** → đối chiếu **mã khóa** với mã script vừa in (hoặc `scs-agent fingerprint`
trên máy) → **Duyệt**. Hostname sai quy ước thì phải nhập mã điểm bán khi duyệt. Agent hỏi lại khoảng 5 phút một lần
và nhận chứng chỉ sau khi được duyệt.

### 2.7 Sau khi cài

```bash
scs-agent status                   # trạng thái đăng ký và thông tin máy sẽ gửi
sudo scs-agent fingerprint         # hostname, thành phần nhận dạng máy, mã khóa
journalctl -u scs-agent -f         # xem log
scs-agent ping                     # kiểm kết nối và chứng chỉ server
sudo bash agent/scripts/install.sh --uninstall          # gỡ dịch vụ, GIỮ khóa máy (cài lại không phải duyệt lại)
sudo bash agent/scripts/install.sh --uninstall --purge  # gỡ hết, máy phải đăng ký và duyệt lại
```

---

## 3. Rule quét log

Chi tiết cách viết rule: [docs/huong-dan-viet-rule.md](docs/huong-dan-viet-rule.md). Tóm tắt trên server đã cài:

```bash
mkdir -p ~/scs-rules
cp ~/SCS-System/server/rules/mau-co-ban.yaml ~/scs-rules/cong-ty.yaml   # chỉ lần đầu; đổi name: cong-ty
/opt/scs-server/scs-server rules check ~/scs-rules/cong-ty.yaml
```

Đưa vào kho rule của server và duyệt (ký), một trong ba cách:

- Web quản trị, trang **"Rule quét log"**: thêm file `.yaml`, server tự chia **Chưa ký / Đã ký / Cần ký lại / Lỗi**, bấm **Duyệt & ký**, rồi **Tải bản ký**.
- Menu server **mục 16**: thêm file, duyệt và ký, xuất bản ký ra thư mục nhà.
- Lệnh: `sudo -u scs /opt/scs-server/scs-server rules list | add | approve | export ... -env-file /etc/scs-server/server.env` (chạy từ thư mục `/`).

Server trong cụm: duyệt bộ mới trên web, cần quá nửa server đồng ý ([hướng dẫn mục 10.1](docs/huong-dan-viet-rule.md#101-kho-bộ-rule-trên-server-duyệt-và-ký)).

Trên máy agent: `sudo scs-agent rules -install cong-ty.json`, rồi `sudo scs-agent rules` để xem bộ đang dùng.

- **Để rule của bạn ngoài thư mục code** (`~/scs-rules/`). Sửa file trong `~/SCS-System/server/rules/` thì cập nhật
  server (menu mục 4) bị từ chối vì "Thư mục code có thay đổi chưa commit".
- Dòng log mẫu dán vào `tests` phải che tên tài khoản, tên máy, IP, tên miền thật.
- Bộ đã ký mà muốn sửa: **tăng `version`** rồi thêm lại (cùng phiên bản không nhận nội dung khác).
- **Phát cho agent:** trang **Rule quét log** → mục "Áp dụng bộ rule cho máy" → chọn bộ đã ký và cấp (tất cả máy /
  một siêu thị / một máy) → **Áp dụng**. Agent nhận ở heartbeat kế tiếp (≤ 10 phút), tự tải, kiểm chữ ký, cài và
  **quét log liên tục** theo bộ đó; sự kiện về server thành giai đoạn OK / cảnh báo. Trang **Máy** hiện "đang quét log".
  Cài tay (`scs-agent rules -install`) vẫn dùng được cho máy chưa gán.

---

## 4. Cụm nhiều server (đang phát triển)

Thiết kế và hiện trạng: [docs/thiet-ke-cum-server.md](docs/thiet-ke-cum-server.md). Hiện các server đã lập cụm, thêm /
cô lập / gỡ server và ra quyết định chung được; **agent chưa chạy theo cụm** và việc duyệt máy vẫn theo từng server.

Làm bằng **menu server** (`bash server/menu.sh`), nhóm "Cụm nhiều server":

**Server đầu tiên (tạo cụm):** mục **13**. Menu hỏi tên cụm, tên server, địa chỉ cổng cụm (gợi ý sẵn IP LAN và cổng 5222),
rồi tự khởi động lại dịch vụ và mở cổng 5222 trên ufw nếu ufw đang bật. Cuối cùng in dòng **Mã khóa**: đưa mã này cho người
cài server tiếp theo. Xem lại bất kỳ lúc nào bằng mục **12**.

**Server mới xin vào cụm:** mục **14**. Nhập địa chỉ cổng cụm và **Mã khóa** của một server đang ở trong cụm, tên server mới,
địa chỉ của nó. Menu gửi yêu cầu, khởi động lại dịch vụ, mở ufw và in **mã khóa của server mới**.

Dùng lệnh thay cho menu (tự động hóa). Chạy bằng user của dịch vụ và chỉ rõ thư mục cụm:

```bash
S="sudo -u scs /opt/scs-server/scs-server"; D="-dir /var/lib/scs-server/data/cluster"
$S cluster init -cluster scs -name hn-01 -addr https://<IP server này>:5222 $D           # server đầu tiên
$S cluster status $D                                                                    # xem cụm, lấy "Mã khóa"
$S cluster join https://<IP server đầu>:5222 -fingerprint <Mã khóa> -name hn-02 -addr https://<IP server này>:5222 $D
sudo systemctl restart scs-server && sudo ufw allow 5222/tcp                             # sau init / join
```

Không chỉ `-dir` mà gõ lệnh từ thư mục khác thì khóa và nhật ký bị tạo **sai chỗ** (đường dẫn mặc định là tương đối), dịch vụ
sẽ không thấy. Sau khi chạy lại cài đặt (menu mục 3) bằng bản này, `server.env` ghi sẵn đường dẫn tuyệt đối và có thể dùng
`-env-file /etc/scs-server/server.env` thay cho `-dir`.

Lệnh `join` so mã khóa trước khi gửi (sai là dừng, chống kẻ đứng giữa) và in **mã khóa của server mới**. Sau đó admin
đăng nhập web của **từng** server trong cụm → trang **Cụm server** → mục "Server chờ vào cụm" → so mã khóa với mã vừa in →
**Duyệt**. Đủ mọi server duyệt thì server mới tự vào cụm trong khoảng 10 giây (kiểm bằng menu mục 12).

**Cô lập / gỡ server** (trang Cụm server):

- **Cô lập tại đây**: server đang mở web ngừng liên lạc với server kia **ngay**, không cần ai đồng ý. Dùng khi nghi bị tấn công.
- **Cô lập toàn cụm / Bỏ cô lập / Gỡ khỏi cụm**: là xác nhận trên server đang mở web; phải xác nhận trên **quá nửa** số server
  (không tính server bị nhắm tới) thì mới có hiệu lực. Server đã gỡ muốn vào lại phải xóa khóa cũ và xin vào từ đầu.

Lưu ý:

- Cụm nên có **từ 3 server, số lẻ**. Cụm 2 server tắt một là không ra được quyết định mới.
- Sao lưu `/var/lib/scs-server/data/cluster/node.key` (khóa riêng của server, không chép sang máy khác). Nhật ký cụm
  (`log.jsonl`) mất thì tải lại được từ server khác.
- Đồng hồ các server phải đúng (NTP).

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
**8** lấy mã đăng nhập web, **9** xem trạng thái và log.

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
- cài lệnh `scs-login-code` để lấy mã đăng nhập web.

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
- **Agent tự chuyển địa chỉ:** mục 3/7 ghi danh sách địa chỉ server (IP LAN, IP WAN, tên miền) vào `SCS_AGENT_SERVERS`.
  Server gửi danh sách này cho agent đã duyệt (lúc duyệt và mỗi heartbeat 5 phút); agent lưu lại, địa chỉ đang dùng
  không kết nối được thì thử ngay địa chỉ kế tiếp. Máy cài bằng gói LAN mang ra ngoài vẫn gọi về được qua IP WAN.
- Cổng web quản trị `7222` **không mở ra Internet**: mặc định chỉ chính máy server vào được. Menu mục **6** (hoặc
  câu hỏi ở mục 3) chọn: **toàn mạng nội bộ** (mọi dải IP riêng 10.x, 172.16–31.x, 192.168.x, hợp với công ty nhiều
  lớp mạng / VLAN định tuyến qua nhau), chỉ dải LAN của server, hoặc tự nhập các dải. Dòng lệnh:
  `sudo bash server/scripts/install.sh --admin-allow private` (hoặc `--admin-allow 10.10.0.0/16`, lặp lại được).
  Script thêm vào `SCS_ADMIN_ALLOW` và mở ufw cho đúng các dải đó nếu ufw đang bật.
- Riêng web quản trị 7222: muốn trình duyệt không báo sai tên, chứng chỉ server phải ghi IP bạn gõ trên trình duyệt (IP LAN, IP VPN):
  menu mục **7** cấp lại chứng chỉ với đủ các IP (CA giữ nguyên, agent không bị ảnh hưởng). Trình duyệt vẫn cảnh báo
  "chưa tin cậy" vì CA là CA nội bộ: nhập file `/etc/scs-server/agent-ca.pem` vào kho chứng chỉ tin cậy của máy
  admin để hết cảnh báo.

### 1.4 Sau khi cài

```bash
scs-login-code                     # lấy mã 6 số để đăng nhập web https://<IP VPN>:7222
journalctl -u scs-server -f        # xem log
sudo bash scripts/install.sh --uninstall   # gỡ dịch vụ (giữ cấu hình, CA, dữ liệu)
```

### 1.5 Cập nhật server

```bash
cd ~/SCS-System
bash server/scripts/update.sh --check      # xem có bản mới không
bash server/scripts/update.sh              # cập nhật lên tag phát hành mới nhất (chưa có tag thì lên main)
bash server/scripts/update.sh --ref v0.2.0 # lên đúng một bản
bash server/scripts/update.sh --rollback   # quay về binary trước lần cập nhật gần nhất
```

Chạy bằng user thường hoặc root (tự gọi sudo khi cài). Script kéo code, chạy test, build; bản mới có đổi cấu trúc DB thì
**sao lưu DB** vào `server/data/backup/` trước; cài xong kiểm `/readyz`, không lên trong 60 giây thì **tự quay về
binary cũ**. Thư mục code có thay đổi chưa commit thì từ chối chạy. Mỗi lần cập nhật cũng build **bản agent cùng
phiên bản** (amd64, arm64); server nhập vào kho, admin phát hành trên web (mục 2).

Khi chạy thật nên đặt `SCS_ENV=prod` trong `/etc/scs-server/server.env` (bắt buộc đủ TLS, CA, mật khẩu DB) rồi
`sudo systemctl restart scs-server`.

---

## 2. SCS-Agent (mỗi máy tại điểm bán)

**Cách hoạt động:** máy tại điểm bán chỉ cài một lần **bản agent khung** (đóng gói trên server). Bản khung đăng ký
với server và chờ admin duyệt. Duyệt xong, agent nhận qua heartbeat (5 phút một lần):

- **cấu hình** đặt trên web, trang **Agent** (dịch vụ cần theo dõi: chung, theo siêu thị, theo máy);
- **bản agent cần chạy**: mỗi lần cập nhật server (menu mục 4) có thêm bản agent cùng phiên bản; admin bấm
  **Phát hành nhóm thử** (các máy đánh dấu "nhóm thử" trong chi tiết máy) rồi **Phát hành tất cả**. Agent tải bản
  mới, kiểm **chữ ký của khóa phát hành** (khóa công khai đóng trong gói, root sở hữu), rồi tự khởi động lại chạy bản
  đó. Bản mới khởi động 3 lần mà chưa gửi được heartbeat thì agent quay về bản khung và không tải lại bản đó.

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

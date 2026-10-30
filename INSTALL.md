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
**1** kiểm tra điều kiện, **2** cài Docker/Go/PostgreSQL/NATS, **3** cài dịch vụ (hỏi IP WAN, IP LAN, dải mạng
được vào web; gợi ý sẵn IP LAN và dải LAN của máy). Về sau: **4** cập nhật, **6** cho thêm dải mạng vào web,
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

- Router phía server **chuyển tiếp một cổng** ra ngoài (ví dụ `16222`) về cổng `6222` của máy server. Agent gọi `https://<IP WAN>:16222`.
- Cổng web quản trị `7222` **không mở ra Internet**: mặc định chỉ chính máy server vào được. Cho máy trong LAN hoặc VPN
  vào: menu mục **6** (hoặc `sudo bash server/scripts/install.sh --admin-allow 192.168.1.0/24`); script thêm dải đó vào
  `SCS_ADMIN_ALLOW` và mở ufw cho đúng dải nếu ufw đang bật.
- Muốn trình duyệt không báo sai tên, chứng chỉ server phải ghi IP mà bạn gõ trên trình duyệt (IP LAN, IP VPN):
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
binary cũ**. Thư mục code có thay đổi chưa commit thì từ chối chạy.

Khi chạy thật nên đặt `SCS_ENV=prod` trong `/etc/scs-server/server.env` (bắt buộc đủ TLS, CA, mật khẩu DB) rồi
`sudo systemctl restart scs-server`.

---

## 2. SCS-Agent (mỗi máy tại điểm bán)

```bash
sudo bash agent/menu.sh
```

Chọn **1** kiểm tra trước (hostname, đồng hồ, kết nối tới server), rồi **2** cài: script hỏi địa chỉ server và file
`agent-ca.pem` (tự tìm file đặt cạnh thư mục `agent/`). Về sau: **3** trạng thái đăng ký, **4** mã khóa máy để
admin đối chiếu khi duyệt, **5** log.

### 2.1 Chuẩn bị

- **Hostname đúng quy ước** (server đọc mã điểm bán và vị trí máy từ đây):
  `12345-TN1-PRI` (máy 1), `12345-TN2-SEC` (máy 2), `12345-TN3`, `12345-TN4`… Đổi: `sudo hostnamectl set-hostname 12345-TN1-PRI`.
- **Binary** `scs-agent` cho Linux. Build một lần (máy nào có Go cũng được) rồi chép sang:
  ```bash
  cd agent && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/scs-agent ./cmd/scs-agent
  ```
- **File `agent-ca.pem`** lấy từ máy server (`/etc/scs-server/agent-ca.pem`).

Chép thư mục `agent/` (có `bin/scs-agent`, `scripts/`, `deploy/`) và `agent-ca.pem` sang máy cần cài.

### 2.2 Kiểm tra trước (không thay đổi gì trên máy)

```bash
sudo bash agent/scripts/install.sh --check --server https://<IP WAN>:16222 --ca agent-ca.pem
```

Kiểm: Ubuntu, systemd, binary chạy được, hostname đúng quy ước, đồng hồ đồng bộ NTP, **kết nối tới server**
và chứng chỉ server khớp CA.

### 2.3 Cài

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

### 2.4 Duyệt máy

Admin vào web quản trị → trang **Chờ duyệt** → đối chiếu **mã khóa** với mã script vừa in (hoặc `scs-agent fingerprint`
trên máy) → **Duyệt**. Hostname sai quy ước thì phải nhập mã điểm bán khi duyệt. Agent hỏi lại khoảng 5 phút một lần
và nhận chứng chỉ sau khi được duyệt.

### 2.5 Sau khi cài

```bash
scs-agent status                   # trạng thái đăng ký và thông tin máy sẽ gửi
sudo scs-agent fingerprint         # hostname, thành phần nhận dạng máy, mã khóa
journalctl -u scs-agent -f         # xem log
sudo bash agent/scripts/install.sh                      # cập nhật binary (giữ cấu hình, khóa máy)
sudo bash agent/scripts/install.sh --uninstall          # gỡ dịch vụ, GIỮ khóa máy (cài lại không phải duyệt lại)
sudo bash agent/scripts/install.sh --uninstall --purge  # gỡ hết, máy phải đăng ký và duyệt lại
```

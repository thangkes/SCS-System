# Cài đặt SCS-System

Hai phần cài riêng, trên hai loại máy khác nhau:

| | Máy | Script | Làm một lần hay nhiều lần |
|---|---|---|---|
| **SCS-Server** | máy server trung tâm (Ubuntu ≥ 22.04) | `server/scripts/setup.sh` rồi `server/scripts/install.sh` | một lần |
| **SCS-Agent** | từng máy Ubuntu tại điểm bán | `agent/scripts/install.sh` | mỗi máy một lần |

Thứ tự: **cài server trước**. Cài server xong mới có file `agent-ca.pem` (CA của server) để mang sang các máy agent.

Cả hai script đều chạy lại nhiều lần được: phần nào đã có thì giữ nguyên, chỉ cập nhật binary và khởi động lại dịch vụ.

---

## 1. SCS-Server

### 1.1 Chuẩn bị máy: Docker, Go, PostgreSQL + NATS

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

### 1.3 Mạng

- Router phía server **chuyển tiếp một cổng** ra ngoài (ví dụ `16222`) về cổng `6222` của máy server. Agent gọi `https://<IP WAN>:16222`.
- Cổng web quản trị `7222` **không mở ra Internet**: chỉ vào qua VPN hoặc trên chính máy server
  (danh sách IP được vào: `SCS_ADMIN_ALLOW` trong `server.env`, ví dụ thêm dải VPN `10.8.0.0/24`).

### 1.4 Sau khi cài

```bash
scs-login-code                     # lấy mã 6 số để đăng nhập web https://<IP VPN>:7222
journalctl -u scs-server -f        # xem log
sudo bash scripts/install.sh --uninstall   # gỡ dịch vụ (giữ cấu hình, CA, dữ liệu)
```

Khi chạy thật nên đặt `SCS_ENV=prod` trong `/etc/scs-server/server.env` (bắt buộc đủ TLS, CA, mật khẩu DB) rồi
`sudo systemctl restart scs-server`.

---

## 2. SCS-Agent (mỗi máy tại điểm bán)

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

# SCS-Agent

Agent chạy trên từng máy siêu thị, gửi trạng thái máy và sự kiện log về server.

- Nền tảng: Ubuntu (systemd), Go, một binary tĩnh (không cgo)
- Kế hoạch: [../docs/ke-hoach-giai-doan-1.html](../docs/ke-hoach-giai-doan-1.html)
- Dùng chung giao thức, fingerprint, quy ước hostname với server qua `../common/`

## Nguyên tắc

- Agent chỉ **chủ động gọi lên** server. Server không gửi lệnh xuống; rule và bản cập nhật agent tự kéo về, kiểm chữ ký.
- Không có bootstrap token: máy mới gửi đăng ký rồi **chờ admin duyệt** trên web.
- Gọi server qua **IP WAN của server**, cổng được chuyển tiếp (port forward) về 6222 của SCS-Server.
- Chạy bằng **user riêng** `scs-agent`, không chạy root.

## Hostname

Phải đúng quy ước, server đọc mã siêu thị và vị trí máy từ đây:

| Máy | Hostname |
|---|---|
| Máy 1 (chính) | `12345-TN1-PRI` |
| Máy 2 (chính thứ hai) | `12345-TN2-SEC` |
| Máy 3 trở đi | `12345-TN3`, `12345-TN4`… |

Sai quy ước thì agent vẫn chạy, server gắn cảnh báo, admin xem rồi quyết khi duyệt.

## Cài đặt

```bash
sudo bash scripts/install.sh --check --server https://<IP WAN>:<cổng> --ca agent-ca.pem   # chỉ kiểm tra
sudo bash scripts/install.sh --server https://<IP WAN>:<cổng> --ca agent-ca.pem [--watch pos.service]
```

Xem [../INSTALL.md](../INSTALL.md) mục 2. Dịch vụ: `deploy/systemd/scs-agent.service`.

Bản cài lần đầu ("bản khung", `/opt/scs-agent/scs-agent`, root sở hữu) kiểm chữ ký rồi chuyển sang bản server phát hành.
Bản khung cũng được thay từ server: `deploy/systemd/scs-agent-promote.timer` chạy `scs-agent promote` bằng root mỗi 30
phút; bản mới đã chạy tốt 1 giờ và qua kiểm chữ ký thì được chép vào `/opt`, giữ bản cũ để tự quay về nếu bản khung mới
không gọi được server (`internal/update/promote.go`).

## Lệnh

```bash
scs-agent fingerprint   # kiểm hostname, xem thành phần nhận dạng máy và mã khóa (đối chiếu khi duyệt)
scs-agent status        # cấu hình, trạng thái đăng ký, trạng thái máy sẽ gửi trong heartbeat
scs-agent run           # dịch vụ systemd gọi lệnh này
scs-agent version
```

Serial phần cứng chỉ root đọc được: chạy `sudo scs-agent fingerprint` để xem đủ.

## Cấu hình: `/etc/scs-agent/agent.env`

```bash
SCS_SERVER=https://203.0.113.10:16222          # IP WAN của server : cổng chuyển tiếp
SCS_WATCH_SERVICES=pos.service             # dịch vụ cần báo trạng thái (tùy chọn)
# SCS_SERVER_CA=/etc/scs-agent/server-ca.pem   # CA của server, đóng sẵn trong gói
# SCS_KEY_DIR=/etc/scs-agent                   # khóa riêng + chứng chỉ (quyền 600)
# SCS_STATE_DIR=/var/lib/scs-agent             # trạng thái, vị trí đọc log, hàng đợi
# SCS_LOG_LEVEL=info
```

## Đăng ký

Lần chạy đầu agent sinh khóa Ed25519 (`/etc/scs-agent/key.pem`, quyền 600), gửi gói đăng ký ký bằng khóa đó,
rồi chờ admin duyệt (hỏi lại mỗi 5 phút ±20%). Được duyệt thì kiểm chứng chỉ (do CA server ký, mang đúng khóa
máy, đúng mã máy) rồi mới lưu `cert.pem`. Bị từ chối thì dừng gửi. Lỗi mạng / server bận thì giãn từ 30 giây
tới 30 phút. Đồng hồ máy lệch thì agent tự bù theo giờ server (header Date).

Agent chỉ tin CA trong `server-ca.pem`: server khác (kể cả chứng chỉ hợp lệ nơi khác) không nói chuyện được.

## Heartbeat gửi gì

Uptime, phiên bản rule, số sự kiện đang chờ gửi, và trạng thái máy: hostname, bản Ubuntu, kernel,
số CPU, tải 1/5/15 phút, RAM và swap, dung lượng từng phân vùng, RAID phần mềm (`/proc/mdstat`:
hỏng ổ, đang đồng bộ lại), đồng bộ giờ NTP, trạng thái các dịch vụ theo dõi. Phần nào không đọc được
thì để trống kèm lý do, không làm hỏng cả heartbeat.

## Các mốc

| Mốc | Nội dung | Trạng thái |
|---|---|---|
| A0 | Khung, cấu hình, lệnh con, khóa máy, fingerprint, trạng thái máy | xong |
| A1 | Ký gói, mTLS, đăng ký → chờ duyệt → nhận chứng chỉ | xong (server M3 xong) |
| A2 | Heartbeat, bù lệch giờ, giãn nhịp khi server bận | cần server M4 |
| A3 | Quét log (journald + file), rule mặc định, hàng đợi trên đĩa, gửi lô | cần server M4 |
| A4 | Kéo rule có chữ ký | cần server M7 |
| A5 | Cài đặt: script + systemd (giới hạn quyền, tài nguyên) xong; gói .deb để sau | một phần |
| A6 | Watchdog, cập nhật, quay về bản cũ | cần server M7 |

## Build

```bash
cd agent
go test ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o bin/scs-agent ./cmd/scs-agent
```

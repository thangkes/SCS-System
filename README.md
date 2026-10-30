# SCS-System

Hệ thống giám sát máy Ubuntu đặt tại nhiều điểm bán: **SCS-Agent** chạy trên từng máy, gửi trạng thái
và sự kiện log về **SCS-Server**; admin duyệt máy và theo dõi trên web quản trị.

*A monitoring system for fleets of Ubuntu machines across many sites: SCS-Agent reports machine status
and matched log events to SCS-Server, where admins approve machines and watch alerts on a web console.
Code comments and docs are in Vietnamese.*

> Trạng thái: đang phát triển (giai đoạn 1). Chưa dùng cho môi trường thật.

## Thành phần

| Thư mục | Nội dung |
|---|---|
| [`server/`](server/) | **SCS-Server** (`scs-server`): cổng nhận dữ liệu agent (6222), web quản trị HTTPS (7222), PostgreSQL + TimescaleDB |
| [`agent/`](agent/) | **SCS-Agent** (`scs-agent`): chạy trên từng máy, đăng ký, gửi heartbeat và sự kiện log |
| [`common/`](common/) | Phần dùng chung: giao thức gói tin có ký, fingerprint máy, quy ước hostname |
| [`docs/`](docs/) | Thiết kế giai đoạn 1 |

## Nguyên tắc thiết kế

- **Agent chỉ chủ động gọi lên server**; server không gửi lệnh xuống. Rule và bản cập nhật agent tự kéo về, kiểm chữ ký.
- **Mọi máy phải được admin duyệt** trước khi gửi được dữ liệu. Không có bí mật dùng chung trong source agent.
- Mỗi máy có **khóa Ed25519 riêng** (không rời máy): ký mọi gói tin và dùng làm chứng chỉ mTLS do CA nội bộ của server cấp.
- Gói tin có giờ và nonce (chống gửi lại), giao tiếp qua TLS 1.3, agent chỉ tin CA của server.
- Dữ liệu lưu trong **PostgreSQL + TimescaleDB**, dữ liệu theo ngày giữ 60 ngày, ràng buộc nằm trong DB.

## Cài đặt

Hướng dẫn đầy đủ, tách riêng server và agent: **[INSTALL.md](INSTALL.md)**.

```bash
# Máy server
bash server/scripts/setup.sh --start --test
sudo bash server/scripts/install.sh --wan-ip <IP WAN> --vpn-ip <IP VPN>

# Mỗi máy tại điểm bán (cần agent-ca.pem lấy từ server)
sudo bash agent/scripts/install.sh --server https://<IP WAN>:<cổng> --ca agent-ca.pem
```

Chi tiết từng thành phần: [server/README.md](server/README.md), [agent/README.md](agent/README.md).

## Build

```bash
go test ./common/... ./server/... ./agent/...
(cd server && CGO_ENABLED=0 go build -trimpath -o bin/scs-server ./cmd/scs-server)
(cd agent  && CGO_ENABLED=0 go build -trimpath -o bin/scs-agent  ./cmd/scs-agent)
```

Cần Go ≥ 1.22 (khuyến nghị bản mới nhất).

## Giấy phép

[Apache License 2.0](LICENSE).

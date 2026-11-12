# SCS-System

Hệ thống giám sát máy Ubuntu đặt tại nhiều điểm bán: **SCS-Agent** chạy trên từng máy, gửi trạng thái
và sự kiện log về **SCS-Server**; admin duyệt máy và theo dõi trên web quản trị.

*A monitoring system for fleets of Ubuntu machines across many sites: SCS-Agent reports machine status
and matched log events to SCS-Server, where admins approve machines and watch alerts on a web console.
Code comments and docs are in Vietnamese.*

> Trạng thái: đang phát triển (giai đoạn 1). Chưa dùng cho môi trường thật.
> Đang chuyển sang **cụm nhiều server ngang hàng**: thiết kế đã chốt ở
> [docs/thiet-ke-cum-server.md](docs/thiet-ke-cum-server.md). Đã có mốc C1 (khóa node, cổng 5222,
> `scs-server cluster init | status | ping | join`), phần lõi C2 (đề nghị, ký quá nửa, bắt kịp nhật ký) và C3
> (thêm / cô lập / gỡ server, trang web "Cụm server"). Agent chưa chạy theo cụm (C4).

## Thành phần

| Thư mục | Nội dung |
|---|---|
| [`server/`](server/) | **SCS-Server** (`scs-server`): cổng nhận dữ liệu agent (6222), web quản trị HTTPS (7222), liên lạc giữa các server trong cụm (5222), PostgreSQL + TimescaleDB |
| [`agent/`](agent/) | **SCS-Agent** (`scs-agent`): chạy trên từng máy, đăng ký, gửi heartbeat và sự kiện log |
| [`common/`](common/) | Phần dùng chung: giao thức gói tin có ký, fingerprint máy, quy ước hostname, khung rule quét log |
| [`docs/`](docs/) | [Kế hoạch giai đoạn 1](docs/ke-hoach-giai-doan-1.html), [hướng dẫn viết rule](docs/huong-dan-viet-rule.md), [thiết kế cụm nhiều server](docs/thiet-ke-cum-server.md) |

## Nguyên tắc thiết kế

- **Agent chỉ chủ động gọi lên server**; server không gửi lệnh xuống. Rule và bản cập nhật agent tự kéo về, kiểm chữ ký.
- **Mọi máy phải được admin duyệt** trước khi gửi được dữ liệu. Không có bí mật dùng chung trong source agent.
- Mỗi máy có **khóa Ed25519 riêng** (không rời máy): ký mọi gói tin và dùng làm chứng chỉ mTLS do CA nội bộ của server cấp.
- Gói tin có giờ và nonce (chống gửi lại), giao tiếp qua TLS 1.3, agent chỉ tin CA của server.
- Dữ liệu lưu trong **PostgreSQL + TimescaleDB**, dữ liệu theo ngày giữ 60 ngày, ràng buộc nằm trong DB.
- **Rule chỉ đọc log và khớp regex** (RE2), không chạy được lệnh trên máy; bộ rule phải có chữ ký.

## Cổng

| Cổng (TCP) | Ai gọi | Dùng cho |
|---|---|---|
| 6222 | agent → server | đăng ký, heartbeat, tải bản agent và bộ rule, gửi sự kiện |
| 7222 | trình duyệt admin → server | web quản trị, chỉ mở cho các dải IP được phép |
| 5222 | server ↔ server | liên lạc trong cụm; chỉ mở khi server đã thuộc cụm hoặc đang xin vào; chỉ nhận khóa server có trong danh sách cụm |

Không dùng UDP. Phía máy agent không phải mở cổng nào.

## Cụm nhiều server (đang làm)

Nhiều SCS-Server chạy ngang hàng, server nào cũng là gốc. Thiết kế đầy đủ:

- **Dữ liệu agent ở server nhận**, không đồng bộ; web quản trị hỏi mọi server và ghép lại. Trạng thái siêu thị chia sẻ chung.
- **Quyết định chung** (duyệt máy, cấu hình, bộ rule, bản agent…) cần chữ ký của **quá nửa** số server hợp lệ; **thêm server cần 100%**.
- **Mỗi server có khóa riêng, không dùng CA chung**: server và agent chỉ tin khóa có trong danh sách cụm có chữ ký.
- **Agent giữ một server** tới khi lỗi mới chuyển, nhận danh sách server theo quy tắc gán server, mỗi 12 giờ hỏi một server khác về danh sách cụm.
- **Cô lập / gỡ** server khi nghi bị tấn công; bản agent cần chữ ký quá nửa server, mỗi chữ ký do admin xác nhận trên chính server đó.

Hiện trạng:

| Mốc | Nội dung | Trạng thái |
|---|---|---|
| C1 | Khóa riêng từng server, chứng chỉ tự ký, cổng 5222 (TLS 1.3 hai chiều, so khóa theo danh sách cụm), nhật ký quyết định | **Đã có** |
| C2 | Đề nghị → các server ký → quá nửa thì ghi và phát; server chậm tự bắt kịp; phát hiện ký hai lần và tách nhánh | **Đã có** phần lõi |
| C2 (phần hai) | Đưa duyệt / thu hồi máy và cấu hình agent vào nhật ký chung | Chưa có |
| C3 | Xin vào cụm qua mạng, admin xác nhận trên từng server, cô lập tại chỗ / toàn cụm, bỏ cô lập, gỡ; trang web "Cụm server" | **Đã có** |
| C4 | Agent theo cụm: tin danh sách cụm thay CA, giữ một server, chuyển kênh, hỏi server khác mỗi 12 giờ | Chưa có |
| C5 | Trạng thái siêu thị chia sẻ chung, web gom dữ liệu từ mọi server | Chưa có |
| C6 | Phát hành bản agent và bộ rule bằng chữ ký của nhiều server | Chưa có |

Tức là hiện nay các server **đã lập cụm, thêm / cô lập / gỡ server và ra quyết định chung được**, nhưng **agent vẫn chạy theo
kiểu một server** (CA nội bộ, duyệt máy trên từng server). Chưa chạy thử trên Ubuntu.

```bash
scs-server cluster init   -cluster scs -name hn-01 -addr https://<IP>:5222          # server đầu tiên: tạo cụm
scs-server cluster status                                                          # xem cụm, lấy "Mã khóa"
scs-server cluster join   https://<IP server đầu>:5222 -fingerprint <Mã khóa> -name hn-02 -addr https://<IP>:5222
scs-server cluster ping   https://<IP>:5222                                        # gọi thử, in mã khóa để đối chiếu
```

Cách lập cụm từng bước: [INSTALL.md mục 4](INSTALL.md#4-cụm-nhiều-server-đang-phát-triển). Thiết kế, mối đe dọa và thứ tự làm:
[docs/thiet-ke-cum-server.md](docs/thiet-ke-cum-server.md).

## Cài đặt

Hướng dẫn đầy đủ, tách riêng server và agent: **[INSTALL.md](INSTALL.md)**.

```bash
bash server/menu.sh          # máy server: menu cài đặt, cập nhật, vận hành (user thường hoặc root)
                             # menu mục 11: đóng gói agent thành một file .tar.gz
sudo bash scs-agent/scripts/install.sh   # mỗi máy tại điểm bán: giải nén gói rồi chạy, không cần nhập gì
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

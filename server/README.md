# SCS-Server

Phía trung tâm: Ingest, Enrollment, Rule, Update, Workers, Admin API (dashboard làm sau).

- Nền tảng: Ubuntu
- Ngôn ngữ: Go (lõi), TypeScript (dashboard)
- Kế hoạch: [../docs/ke-hoach-giai-doan-1.html](../docs/ke-hoach-giai-doan-1.html)

## Cài đặt tự động

```bash
cd github.com/thangkes/SCS-System/server
bash scripts/setup.sh --check     # chỉ quét điều kiện, không cài gì
bash scripts/setup.sh --start     # cài Docker, Go, tạo .env, khởi động Postgres + NATS
bash scripts/setup.sh --test      # cài xong chạy go test (gộp được: --start --test)
```

Script tự kiểm tra Ubuntu ≥ 22.04, sudo, dung lượng, RAM, đồng bộ giờ, cổng trống và mạng tới nguồn tải;
cài Go bản chính thức (kiểm SHA256), Docker + compose từ kho Ubuntu; tạo `.env` và sinh mật khẩu
còn trống (không ghi đè giá trị đã có). Lỗi nào cũng báo kèm cách xử lý; log ở `server/setup.log`.
Chạy lại nhiều lần được.

## Chạy thử trên Ubuntu (cài tay)

Cần Go ≥ 1.22 (nên cài bản mới từ https://go.dev/dl thay vì `apt`) và Docker có plugin compose
(`sudo apt install docker.io docker-compose-v2`, rồi `sudo usermod -aG docker $USER` và đăng nhập lại).

```bash
cd github.com/thangkes/SCS-System/server
cp .env.example .env && chmod 600 .env
# Điền SCS_DB_PASSWORD, SCS_NATS_PASSWORD (tạo bằng: echo "s$(openssl rand -hex 24)"; mật khẩu NATS phải bắt đầu bằng chữ cái)

docker compose -f compose.dev.yml up -d  # Postgres + NATS, dùng chung mật khẩu trong .env
docker compose -f compose.dev.yml ps     # chờ cả hai báo (healthy)

go test ../common/... ./...
SCS_INGEST_LISTEN=127.0.0.1:6222 go run ./cmd/scs-server all   # chạy tất cả trong một tiến trình
```

Chưa có PKI (thiếu `SCS_TLS_CERT` hoặc `SCS_TLS_CLIENT_CA`) thì cổng agent **chỉ được chạy ở loopback**:
nghe ra ngoài thì server từ chối khởi động, để quên đặt `SCS_ENV=prod` cũng không thành mở toang.
Cần thử từ máy khác trên mạng tin cậy thì đặt `SCS_DEV_INSECURE=1` trong `.env` (prod cấm bật).

Postgres và NATS chỉ mở cổng ở `127.0.0.1`. Trang giám sát NATS: http://127.0.0.1:8222.
Đổi `SCS_DB_PASSWORD` sau khi DB đã tạo thì phải xóa dữ liệu dev: `docker compose -f compose.dev.yml down -v`.

Ở terminal khác:

```bash
curl http://127.0.0.1:6222/healthz       # cổng cho agent
curl -k https://127.0.0.1:7222/readyz    # web quản trị (HTTPS, dev tự ký nên cần -k)
go run ./cmd/scs-server login-code   # lấy mã 6 số rồi mở https://127.0.0.1:7222/
```

Build binary cho Linux:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o bin/scs-server ./cmd/scs-server
```

## Lệnh con

| Lệnh | Việc | Cổng mặc định |
|---|---|---|
| `ingest` | Cổng duy nhất cho agent: đăng ký (không cần chứng chỉ), heartbeat, log, gia hạn chứng chỉ, tải rule và gói cập nhật (bắt buộc chứng chỉ client khi có `SCS_TLS_CLIENT_CA`) | `:6222` |
| `admin` | Web HTTPS xác nhận máy và theo dõi, đăng nhập bằng mã 6 số; luôn HTTPS (dev tự ký); chỉ nhận kết nối từ `SCS_ADMIN_ALLOW` (mặc định localhost, thêm dải VPN) | `:7222` |
| `worker` | Xử lý hàng đợi | — |
| `all` | Chạy tất cả (dev) | — |
| `login-code` | In mã đăng nhập web hiện hành (chạy trên máy server, cùng user với dịch vụ); `-watch` để tự in mã mới; `-unlock` mở khóa khi bị khóa do nhập sai nhiều | — |
| `pki` | Tạo CA nội bộ, chứng chỉ server, khóa phát hành: `pki init`, `pki server`, `pki release` | — |
| `rules` | Viết rule quét log: `rules check`, `rules test`, `rules build`; kho bộ rule trên server (`SCS_RULES_DIR`): `rules list`, `add`, `approve`, `remove`, `export` ([hướng dẫn](../docs/huong-dan-viet-rule.md)) | — |
| `cluster` | Cụm nhiều server: `cluster init`, `status`, `ping`, `join` (xem mục "Cụm nhiều server" bên dưới) | `:5222` |
| `version` | In phiên bản | — |

Cấu hình đọc từ biến môi trường, tự nạp `./.env` nếu có, hoặc chỉ định `-env-file /etc/scs-server/server.env`.
Biến đã có sẵn trong môi trường thắng giá trị trong file.

## Cụm nhiều server

Thiết kế: [docs/thiet-ke-cum-server.md](../docs/thiet-ke-cum-server.md). Cách lập cụm từng bước: [INSTALL.md mục 4](../INSTALL.md).
Đã có: lập cụm, đề nghị và ký quá nửa, thêm / cô lập / gỡ server, trang web "Cụm server". Chưa có: agent theo cụm,
duyệt máy qua cụm, trạng thái chung, phát hành bằng chữ ký nhiều server.

| Thứ | Ở đâu |
|---|---|
| Định dạng quyết định, luật ký và bỏ phiếu (dùng chung với agent sau này) | `common/cluster/` |
| Khóa node, nhật ký, đồng thuận, thêm / cô lập / gỡ node, cổng 5222 | `server/internal/cluster/` |
| API cho trang web | `server/internal/app/cluster.go` (`/admin/v1/cluster...`) |
| Lệnh `scs-server cluster` | `server/cmd/scs-server/cluster.go` |

Thư mục cụm (`SCS_CLUSTER_DIR`, mặc định `data/cluster`, trên server đã cài là `/var/lib/scs-server/data/cluster`):

| File | Nội dung |
|---|---|
| `node.key` | Khóa riêng Ed25519 của server (quyền 600). **Không chép sang máy khác.** Mất khóa thì server phải xin vào cụm lại bằng khóa mới |
| `log.jsonl` | Nhật ký quyết định, mỗi dòng một quyết định có chữ ký. Mở lại là kiểm lại toàn bộ; mất thì tải lại được từ server khác |
| `votes.json` | Phiếu server đã bỏ cho quyết định kế tiếp (ghi trước khi trả chữ ký, để khởi động lại không ký trùng) |
| `local.json` | Riêng của server này: yêu cầu xin vào cụm đang chờ, xác nhận của admin, cô lập tại chỗ |
| `join.json` | Có khi server đang xin vào cụm; tự xóa khi vào xong |

Cổng 5222 (`SCS_CLUSTER_LISTEN`) chỉ mở khi có `log.jsonl` (đã thuộc cụm) hoặc `join.json` (đang xin vào), và chỉ khi chạy
lệnh `all`. TLS 1.3 hai chiều; bên gọi phải có chứng chỉ, và trừ `hello` / `join` / `join-sign` thì khóa phải có trong danh sách cụm:

| Đường dẫn | Ai gọi được | Việc |
|---|---|---|
| `GET /cluster/v1/hello` | ai có chứng chỉ | mã node, mã khóa, mã cụm rút gọn (để đối chiếu trước khi xin vào) |
| `POST /cluster/v1/join` | ai có chứng chỉ (tối đa 20 yêu cầu chờ) | xin vào cụm; phải được admin duyệt trên mọi server |
| `POST /cluster/v1/join-sign` | ai có chứng chỉ | chỉ server đang xin vào trả lời: ký quyết định thêm chính nó |
| `GET /cluster/v1/status` | server trong cụm | trạng thái cụm, tách nhánh, server ký hai lần |
| `POST /cluster/v1/propose`, `commit` | server trong cụm | xin chữ ký cho đề nghị; phát quyết định đã đủ chữ ký |
| `GET /cluster/v1/log?from=N` | server trong cụm | đọc nhật ký từ quyết định số N (bắt kịp) |
| `POST /cluster/v1/join-forward` | server trong cụm | chuyển yêu cầu xin vào cụm cho server khác |

Cách dùng chính là **menu server** (`bash server/menu.sh`, mục 12–14): menu chạy lệnh bằng đúng user, đúng thư mục, rồi khởi
động lại dịch vụ và mở ufw. Gõ tay thì lệnh `cluster` đọc và ghi thẳng thư mục cụm, nên phải chạy bằng user của dịch vụ và
chỉ rõ thư mục: `sudo -u scs /opt/scs-server/scs-server cluster status -dir /var/lib/scs-server/data/cluster`
(đường dẫn mặc định `data/cluster` là tương đối, gõ từ thư mục khác sẽ ra sai chỗ). Sau `cluster init` / `cluster join` phải
khởi động lại dịch vụ để mở cổng cụm.

Test: `go test ./common/cluster/ ./server/internal/cluster/` (cụm 3, 4, 5 server trong bộ nhớ và qua TLS thật).

## Trạng thái

Mỗi lệnh đều có `/healthz` (tiến trình còn sống) và `/readyz` (kiểm PostgreSQL bằng `SELECT 1`;
DB chưa lên thì trả `503`, chi tiết lỗi nằm trong log). Server vẫn khởi động khi DB chưa lên.
Route còn trả `501` kèm mốc sẽ làm: `GET /v1/rules/{version}` (M7), `POST /v1/cert/renew` (M3, gia hạn chứng chỉ),
`GET /admin/v1/devices/{id}` (M6). `/readyz` chưa kiểm NATS.
`/readyz` dùng lại kết quả kiểm trong 2 giây (gọi dồn dập không làm cạn pool DB). `/healthz` trên cổng
agent (công khai) không trả phiên bản.

## Đăng ký máy (agent) và chứng chỉ

1. **Tạo CA và chứng chỉ server** (một lần, trên máy server):
   ```bash
   scs-server pki init -ip <IP WAN của server> -ip <IP VPN để vào web>
   ```
   Lệnh tạo `data/pki/ca.pem`, `ca.key` (quyền 600, giữ kín), `server.pem`, `server.key` và in các dòng
   `SCS_TLS_*`, `SCS_CA_*` cần điền vào `.env`. Đổi IP WAN: xóa `server.pem/server.key` rồi `scs-server pki server -ip ...`.
2. **Router phía server** chuyển tiếp cổng ra ngoài (ví dụ 16222) về 6222 của máy server.
3. **Mỗi máy siêu thị**: chép `ca.pem` thành `/etc/scs-agent/server-ca.pem`, đặt
   `SCS_SERVER=https://<IP WAN>:<cổng>` trong `/etc/scs-agent/agent.env`, chạy agent.
4. Agent gửi đăng ký (ký bằng khóa riêng của máy) → máy hiện ở trang **Chờ duyệt** trên web, kèm mã khóa
   để đối chiếu với `scs-agent fingerprint` trên máy. Admin bấm **Duyệt** (hostname sai thì nhập mã siêu thị).
5. Agent hỏi lại sau tối đa ~5 phút và nhận chứng chỉ client (90 ngày), cấp từ chính khóa máy đã gửi
   (không cần CSR, khóa riêng không rời máy). Từ đó mọi request khác dùng mTLS.

Không có bootstrap token: mọi máy đều phải được admin duyệt. Mỗi IP gửi đăng ký tối đa 20 lần liền rồi
1 lần mỗi 20 giây; tối đa 5.000 yêu cầu chờ duyệt cùng lúc. Gói đăng ký phải có giờ lệch không quá 5 phút
(agent tự bù theo giờ server), chữ ký đúng bằng khóa trong gói, fingerprint khớp các thành phần.
Máy đã duyệt hỏi lại thì được cấp chứng chỉ mới; DB chỉ giữ serial mới nhất.

## Cơ sở dữ liệu

PostgreSQL + TimescaleDB là nơi lưu chính. Server tự cập nhật cấu trúc DB khi khởi động (migration nhúng trong
binary, `internal/store/migrations/`); DB chưa lên thì thử lại mỗi 5 giây, `/readyz` báo `schema: lỗi` tới khi xong.

| Bảng | Nội dung | Giữ |
|---|---|---|
| `stores` | mã siêu thị, IP WAN cố định | lâu dài |
| `devices` | hostname `<mã>-TN1-PRI`, `<mã>-TN2-SEC`, `<mã>-TN3`…, khóa, fingerprint, trạng thái duyệt | lâu dài |
| `device_status` | trạng thái mới nhất mỗi máy (một dòng, ghi đè mỗi heartbeat) | bản mới nhất |
| `device_status_changes` | lịch sử trạng thái, **chỉ khi đổi đáng kể** (OS, kernel, agent, bộ rule, IP, dịch vụ, RAID, đồng bộ giờ, ổ đầy 80/90/95%) | 60 ngày |
| `device_periods` | **giai đoạn** OK / cảnh báo / mất liên lạc của từng máy, kèm tóm tắt theo rule (số lần, dòng log mẫu); mỗi máy một giai đoạn mở | 60 ngày sau khi đóng |
| `alerts` | cảnh báo hiện trên web (giai đoạn cảnh báo, mất liên lạc, máy chờ duyệt, hostname sai, IP lạ) | 60 ngày |
| `agent_releases`, `agent_rollout`, `agent_config` | bản agent, nhóm phát hành, cấu hình / nhãn máy 3 cấp | lâu dài |
| `audit` | nhật ký thao tác, chỉ ghi thêm | lâu dài |

Mọi máy đăng ký đều chờ admin duyệt. Ràng buộc nằm trong DB: hostname hợp lệ phải đúng quy ước PRI/SEC, mỗi vị trí
TN trong siêu thị và mỗi hostname chỉ một máy đang hoạt động, mỗi gói có nonce (gửi lại không ghi trùng).

Test cấu trúc DB chạy trên DB riêng `scs_test`: `bash scripts/setup.sh --start --test` tự tạo DB và chạy, hoặc
`SCS_TEST_DATABASE_URL='postgres://scs:<mật khẩu>@127.0.0.1:5432/scs_test?sslmode=disable' go test ./internal/store/`.

## Lưu theo giai đoạn

Thiết kế: [docs/thiet-ke-luu-theo-giai-doan.md](../docs/thiet-ke-luu-theo-giai-doan.md). Không lưu từng sự kiện, từng heartbeat:

- Sự kiện `info` / `low` / `medium` cộng vào giai đoạn đang mở; `high` / `critical` đóng OK, mở **cảnh báo**.
- Cảnh báo high tự đóng sau `SCS_ALERT_AUTOCLOSE` (mặc định 24h) không có sự kiện nặng mới; đã lên critical thì chỉ
  admin đóng (web: **Đã xử lý**). Quá `SCS_OFFLINE_AFTER` (15m) không nghe máy: **mất liên lạc**, tự đóng khi máy gửi lại.
- Agent gửi sự kiện qua `POST /v1/logs` (lô đã ký, tối đa 500 sự kiện; agent chưa gửi, mốc A3). Tiến trình `worker`
  (hoặc `all`) kiểm mỗi phút.
- API web: `GET /admin/v1/devices/{id}/periods`, `POST /admin/v1/periods/{id}/resolve`.

## Chạy lâu dài: dịch vụ systemd

Để SCS-Server tự chạy khi khởi động lại máy và tự bật lại khi chết:

```bash
bash scripts/setup.sh --start          # lần đầu: cài Docker/Go, tạo .env, chạy Postgres + NATS
sudo bash scripts/install.sh --wan-ip <IP WAN> [--vpn-ip <IP VPN>]   # cài dịch vụ + CA (chạy lại để cập nhật binary)
scs-login-code                         # lấy mã đăng nhập web
journalctl -u scs-server -f            # xem log
```

- Dịch vụ chạy bằng user `scs`, cấu hình ở `/etc/scs-server/server.env` (chép từ `server/.env` lần đầu,
  sau đó sửa file này), dữ liệu ở `/var/lib/scs-server`, binary ở `/opt/scs-server`.
- `Restart=always`: chết thì bật lại sau 10 giây. Không cần chờ Docker: DB chưa lên thì `/readyz` báo 503
  rồi tự kết nối khi DB lên. Postgres và NATS có `restart: unless-stopped` nên cũng tự lên lại.
- Chứng chỉ TLS server tự nạp lại khi file đổi (step-ca gia hạn), không cần restart; còn dưới 7 ngày thì cảnh báo
  trong log. Đổi client CA thì phải restart.
- Log: SCS-Server ghi vào journald (tự xoay vòng); container Docker giữ tối đa 5 × 10 MB mỗi container.
- Khởi động lại server thì mọi người đăng nhập lại (phiên nằm trong RAM) và mã đăng nhập đổi.
- Gỡ dịch vụ: `sudo bash scripts/install.sh --uninstall` (giữ cấu hình và dữ liệu).

## Chống quá tải và tấn công trên cổng agent (6222)

Cổng 6222 mở ra Internet nên có nhiều lớp giới hạn, request không hợp lệ bị loại càng sớm càng tốt:

| Lớp | Giới hạn | Vượt thì |
|---|---|---|
| Kết nối đang mở | 64 / IP (IPv6 tính theo /64), 40.000 tổng (`SCS_INGEST_MAX_CONNS_PER_IP`, `SCS_INGEST_MAX_CONNS`) | đóng ngay, trước bắt tay TLS |
| Kết nối nhàn rỗi | giữ tối đa 30 giây; chỉ HTTP/1.1 (tắt HTTP/2 để một kết nối không chạy 250 request song song) | đóng |
| Chứng chỉ client | mọi route trừ `/v1/enroll` và health | 401 |
| Body theo route | enroll, gia hạn 16 KB; heartbeat 64 KB; logs 2 MB; GET 1 KB | 413 ngay, không đọc body |
| Xử lý đồng thời | enroll 32, agent 512, logs 64 | 503 + `Retry-After` + `next_poll_s` để agent giãn nhịp |
| Log | request thành công ở mức debug; request lỗi cùng mã và lỗi bắt tay TLS (bot quét cổng) trong 30 giây chỉ ghi chi tiết lần đầu, còn lại gộp một dòng | — |

Mọi response đều có `nosniff`, chặn iframe và CSP; `X-Request-Id` luôn do server sinh (không nhận từ client).
Khi chạy bằng systemd, đặt `LimitNOFILE` lớn hơn `SCS_INGEST_MAX_CONNS` (ví dụ 65536).

## Giao diện web

Mở `https://<IP-server>:7222/` (qua VPN hoặc trên máy server), nhập mã 6 số và tên của bạn.

### Đăng nhập bằng mã 6 số (giống NASfone)

```bash
scs-server login-code          # hoặc: go run ./cmd/scs-server login-code
scs-server login-code -watch   # tự in mã mới mỗi phút, Ctrl+C để thoát
```

- Hai mã: **Admin** (toàn quyền) và **Chỉ xem** (mọi thao tác ghi bị chặn 403). Hai mã không bao giờ trùng nhau.
- Mã đổi đầu mỗi phút, mã cũ còn dùng được thêm 10 giây. Mỗi mã dùng một lần; dùng mã Admin thì chỉ mã Admin đổi.
- Sai 5 lần thì đổi mã sớm. Mỗi IP sai tối đa 10 lần / 15 phút; toàn hệ thống 20 lần / 5 phút thì khóa đăng nhập
  5 phút, bị khóa lại liên tiếp thì gấp đôi (10, 20, 40, tối đa 60 phút), yên 1 giờ thì về 5 phút.
  Lượt thử được tính ngay khi request tới, nên gửi nhiều request cùng lúc cũng không vượt được giới hạn.
- Đang bị khóa thì `login-code` báo, kèm log mức error (có thể đang bị dò mã). Mở khóa ngay: `scs-server login-code -unlock`.
- Phiên là cookie HttpOnly, Secure, SameSite=Strict, không lưu lâu: tắt trình duyệt là mất, server cắt sau tối đa 24 giờ.
  Phiên nằm trong RAM: khởi động lại server thì mọi người đăng nhập lại.
- Lệnh `login-code` lấy mã từ server đang chạy qua Unix socket `SCS_CONTROL_SOCKET` (mặc định
  `data/run/control.sock`, socket 600 trong thư mục 700; thư mục có sẵn mà quyền rộng hơn thì server từ chối chạy):
  phải SSH vào được server bằng đúng user chạy dịch vụ mới đọc được mã.
- Không chạy web quản trị sau reverse proxy và không chuyển tiếp cổng 7222 trên máy server: danh sách
  `SCS_ADMIN_ALLOW` xét IP nguồn, mà mọi thứ chạy trên máy server đều mang IP 127.0.0.1. Request có header
  của proxy (`X-Forwarded-For`, `Forwarded`, ...) bị trả 403.
- Tên nhập lúc đăng nhập được ghi vào log (đăng nhập, đăng xuất, thu hồi phiên). Admin xem và thu hồi phiên ở trang **Hệ thống**.
- `SCS_ADMIN_TOKEN` (tùy chọn) chỉ dành cho script gọi API bằng `Authorization: Bearer`, có quyền Admin.

### Mã nguồn giao diện

Giao diện nằm trong
`internal/web/static/` (HTML/CSS/JS thuần, nhúng vào binary bằng `go:embed`, không cần Node).
Có CSP chặt: không script/style inline, không tải gì từ nguồn ngoài.

Trang **Hệ thống** dùng dữ liệu thật (`/healthz`, `/readyz`). Các trang Tổng quan, Máy, Cảnh báo,
Chờ duyệt, Token đang dùng **dữ liệu mẫu** (có ghi rõ trên trang) cho tới khi admin API xong (M6).

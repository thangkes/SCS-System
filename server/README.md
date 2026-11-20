# SCS-Server

Con nhện ở trung tâm của **SCS (Spider Control Scanning)**. Phía trung tâm: Ingest, Enrollment, Rule, Update, Workers, web quản trị và Admin API (đăng nhập bằng mã SSH hoặc tài khoản Gmail, 8 mức quyền).

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
| `admin` | Web HTTPS xác nhận máy và theo dõi, đăng nhập bằng mã 6 số (lấy qua SSH, hoặc tài khoản email nhận mã qua Gmail); luôn HTTPS (dev tự ký); chỉ nhận kết nối từ `SCS_ADMIN_ALLOW` (mặc định localhost, thêm dải VPN) | `:7222` |
| `worker` | Xử lý hàng đợi | — |
| `all` | Chạy tất cả (dev) | — |
| `mail-test` | Gửi thư thử bằng cấu hình `SCS_SMTP_*` (`-to <email>`), để biết Gmail gửi mã đã đúng chưa; Gmail từ chối đăng nhập thì báo dùng mật khẩu ứng dụng | — |
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
Route còn trả `501` kèm mốc sẽ làm: `GET /admin/v1/devices/{id}` (M6). `POST /v1/cert/renew` không dùng: gia hạn
chứng chỉ máy đi qua `POST /v1/enroll` — máy đã duyệt gửi lại gói đăng ký bằng chính khóa máy thì được cấp chứng chỉ
mới 90 ngày (serial cũ hết hiệu lực, hạn lưu ở `devices.cert_expires`, migration 0012, nhật ký `device.cert_issued`).
Agent bản mới tự làm khi chứng chỉ còn dưới 30 ngày (thử lại mỗi giờ; đã hết hạn thì thử mỗi lần gọi, không gửi
chứng chỉ hết hạn lúc bắt tay TLS); lần cấp phải giữ lượt tải. Trang Agent có thẻ **Chứng chỉ máy** (đã hết hạn / còn
dưới 30 ngày / không rõ, hạn sớm nhất); chi tiết máy hiện hạn chứng chỉ. `/readyz` chưa kiểm NATS.
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
| `stores` | mã siêu thị, IP WAN chính + IP WAN thêm, thông tin nhập trên web: tên, tổ chức, quản lý, phó quản lý, trưởng nhóm, phó phòng, ghi chú | lâu dài |
| `devices` | hostname `<mã>-TN1-PRI`, `<mã>-TN2-SEC`, `<mã>-TN3`…, khóa, fingerprint, trạng thái duyệt | lâu dài |
| `device_status` | trạng thái mới nhất mỗi máy (một dòng, ghi đè mỗi heartbeat), kèm bộ rule máy báo đang cài (`rules`) | bản mới nhất |
| `device_status_changes` | lịch sử trạng thái, **chỉ khi đổi đáng kể** (OS, kernel, agent, bộ rule, IP, dịch vụ, RAID, đồng bộ giờ, ổ đầy 80/90/95%) | 60 ngày |
| `device_periods` | **giai đoạn** OK / cảnh báo / mất liên lạc của từng máy, kèm tóm tắt theo rule (số lần, dòng log mẫu); mỗi máy một giai đoạn mở | 60 ngày sau khi đóng |
| `alerts` | cảnh báo hiện trên web (giai đoạn cảnh báo, mất liên lạc, máy chờ duyệt, hostname sai, IP lạ) | 60 ngày |
| `agent_releases`, `agent_rollout`, `agent_config` | bản agent, nhóm phát hành, cấu hình / nhãn máy 3 cấp | lâu dài |
| `accounts` | tài khoản web quản trị: tên đăng nhập, email, trạng thái (chờ duyệt / hoạt động / khóa), mức quyền 1–7, ai duyệt, lần đăng nhập cuối. Không có mật khẩu, không lưu mã | lâu dài |
| `settings` | cài đặt đổi trên web: công tắc duyệt tự động | lâu dài |
| `rollout_progress` | tiến độ phát hành bản agent, 5 phút một mốc (biểu đồ trang Agent) | 90 ngày |
| `server_metrics` | thông số máy server mỗi phút: CPU, RAM, ổ đĩa, lưu lượng mạng, heartbeat, dung lượng DB (trang Báo cáo server) | `SCS_METRICS_KEEP`, mặc định 90 ngày |
| `audit` | nhật ký thao tác, chỉ ghi thêm (cả đăng ký, duyệt, đổi quyền, xóa tài khoản, sửa / nạp thông tin siêu thị) | lâu dài |

Mọi máy đăng ký đều phải được duyệt: bởi admin, hoặc **duyệt tự động** khi bật công tắc ở trang Chờ duyệt (mặc định
tắt; đổi cần mức Agent). Duyệt tự động chỉ khi đủ cả 4 điều kiện: hostname đúng quy ước; siêu thị đã có thông tin trong
dữ liệu đã nạp (form hoặc file); máy gửi từ một IP WAN có trong thông tin siêu thị; không trùng hostname / vị trí TN với
máy đang hoạt động. Thiếu điều kiện thì máy chờ người duyệt, lý do hiện trên thẻ máy; server kiểm lại mỗi lần máy hỏi
lại (5 phút ±20%), nên nạp thông tin siêu thị xong thì lần hỏi kế tiếp được duyệt. Người duyệt ghi là "duyệt tự động",
nhật ký thao tác ghi kèm IP. Ràng buộc nằm trong DB: hostname hợp lệ phải đúng quy ước PRI/SEC, mỗi vị trí
TN trong siêu thị và mỗi hostname chỉ một máy đang hoạt động, mỗi gói có nonce (gửi lại không ghi trùng).

Test cấu trúc DB chạy trên DB riêng `scs_test`: `bash scripts/setup.sh --start --test` tự tạo DB và chạy, hoặc
`SCS_TEST_DATABASE_URL='postgres://scs:<mật khẩu>@127.0.0.1:5432/scs_test?sslmode=disable' go test ./internal/store/`.
**Mỗi lần cập nhật** (`update.sh`, menu mục 4) cũng tự chạy các test này trên `scs_test` (PostgreSQL + TimescaleDB thật
của compose) **trước khi cài**: migration mới hay câu SQL lỗi thì dừng, không cài, code trở về bản cũ (log:
`/tmp/scs-update-test.log`). Migration gặp deadlock / xung đột tuần tự (ví dụ xóa hypertable đúng lúc tác vụ nền
TimescaleDB chạy) thì tự chạy lại file đó, tối đa 5 lần. Ràng buộc DB viết sao cho NULL không lọt qua (CHECK coi NULL
là đạt — migration 0014 sửa `devices_name_parts`). Test xóa và tạo
lại bảng nên chỉ chạy trên DB có chữ "test" trong tên (tự từ chối DB khác).

## Sao lưu DB

- **Định kỳ**: `scs-server-backup.timer` chạy **ngày 1 và 16 hằng tháng lúc 02:30** (≈ 15 ngày một lần; máy tắt lúc đó
  thì chạy bù khi bật lại), gọi `/opt/scs-server/backup-db.sh` bằng root. Giữ `SCS_BACKUP_KEEP` bản định kỳ mới nhất
  (mặc định 6 ≈ 3 tháng). `install.sh` (menu mục 3, mục 4) cài và bật timer.
- **Trước mỗi lần cập nhật có đổi cấu trúc DB** (`update.sh`) và **sao lưu tay** (menu mục 18 → 1) không bao giờ bị tự xóa.
- File `server/data/backup/scs-<ngày-giờ>-<dinh-ky|tay|truoc-…>.sql.gz` (`pg_dump`, nén gzip), quyền 600, thư mục 700.
  Script ghi file tạm, kiểm gzip, kiểm có dòng kết thúc của pg_dump rồi mới đổi tên; thiếu chỗ (dưới 2 lần bản trước)
  thì không chạy. Lỗi thì `systemctl status scs-server-backup` báo failed, menu mục 18 hiện cảnh báo.
- Bản sao lưu nằm **trên cùng ổ với DB**: hỏng ổ / mất máy là mất cả hai. Nên chép định kỳ ra máy khác (ổ ngoài,
  NAS) — bản sao lưu chứa toàn bộ dữ liệu (thông tin siêu thị, tài khoản, nhật ký), giữ kín như DB.
- **Khôi phục** (ghi đè toàn bộ dữ liệu; menu mục 18 → 3 in đủ lệnh): dừng `scs-server` → `dropdb` / `createdb` →
  `CREATE EXTENSION timescaledb; SELECT timescaledb_pre_restore();` → `gzip -dc <file> | psql` →
  `SELECT timescaledb_post_restore();` → bật lại `scs-server`. Chưa diễn tập khôi phục trên máy thật: nên thử một lần
  trên máy / DB thử trước khi cần dùng.

## Sức khỏe máy (A2) — chỉ để tham khảo

Agent bản mới gửi trạng thái máy trong mỗi heartbeat. Server **không tin agent** (cắt số phần tử, làm sạch chuỗi) rồi
liệt kê các **ghi nhận** — **không chấm mức** (không có Nguy hiểm / Cần chú ý), máy chỉ "có ghi nhận" hoặc "không có":

| Ghi nhận khi |
|---|
| Phân vùng ext4 / xfs / btrfs… đang chỉ đọc |
| Phân vùng đầy ≥ 90%; inode ≥ 95% |
| Ổ có lỗi I/O từ lần khởi động (`/sys/block/*/device/ioerr_cnt`) |
| RAID phần mềm thiếu ổ |
| Dịch vụ theo dõi `failed` / `inactive` |
| Nhiệt độ ≥ 90°C; đồng hồ chưa đồng bộ NTP; RAM còn dưới 5% liên tiếp 3 heartbeat |

- Lưu ở `device_status.health` (migration 0016, `{"level": "ok" | "note", "issues": [{code, text}]}`), ghi đè mỗi
  heartbeat; agent không gửi trạng thái thì giữ giá trị cũ; hết thì ghi nhận tự mất.
- **Tách hẳn khỏi cảnh báo**: không ghi sự kiện, không mở giai đoạn cảnh báo, không vào trang Cảnh báo, không tô viền
  siêu thị. Bản 6963dd3 từng ghi vấn đề sức khỏe thành sự kiện `may.<mã>`; migration 0017 đóng các cảnh báo đang mở
  chỉ gồm sự kiện đó (cảnh báo có lẫn sự kiện log giữ nguyên).
- Web: menu riêng **Sức khỏe máy** (nhóm **Tham khảo**, mọi mức quyền xem được): số máy có / không có ghi nhận / chưa
  gửi, các loại ghi nhận đang có (bấm để lọc), bảng máy kèm ghi nhận, tìm theo mã siêu thị / hostname / nội dung, bấm
  máy mở chi tiết. Chi tiết máy có thẻ **Tình trạng máy** (ghi nhận, RAM / swap / từng phân vùng, ổ vật lý + lỗi I/O,
  RAID, dịch vụ, NTP, phần không đọc được); trang **Máy** có cột **Sức khỏe** và ô "Chỉ máy có ghi nhận sức khỏe".
- **SMART** (agent bản mới + tác vụ `scs-agent-smart` trên máy, xem `agent/README.md`): thẻ Tình trạng máy có mục SMART
  từng ổ; ghi nhận tham khảo khi ổ tự báo hỏng, SSD dùng ≥ 90% tuổi thọ, có sector đã thay / chờ thay / không đọc được,
  lỗi media NVMe, lỗi CRC (cáp — số cộng dồn cả đời ổ). Ổ không đọc được SMART (card RAID phần cứng, USB, SAS) chỉ hiện lý do.

## Chứng chỉ máy: server tự kiểm

Worker kiểm 10 phút một lần (migration 0018 thêm hai loại cảnh báo):

- chứng chỉ còn **dưới 14 ngày** mà máy chưa gia hạn (agent bản mới tự gia hạn từ 30 ngày) → **"Chứng chỉ máy sắp hết
  hạn"** (cao); đã **hết hạn** → **"đã hết hạn"** (nghiêm trọng, máy không gửi được gì nữa);
- gia hạn xong, hoặc máy bị thu hồi → tự đóng.

Agent tự xin lại chứng chỉ khi server không nhận chứng chỉ còn hạn (tối đa mỗi giờ một lần; máy bị thu hồi thì server không
cấp). Trên máy: `sudo -u scs-agent scs-agent renew` xin cấp lại ngay; `scs-agent status` hiện hạn chứng chỉ.

## Cài lại máy: duyệt và thay thế máy cũ

Cài lại Ubuntu thì khóa máy mới → yêu cầu đăng ký mới. Máy chờ duyệt trùng hostname hoặc vị trí TN với máy đang hoạt
động: thẻ chờ duyệt hiện **"Cài lại máy"**, so sánh phần cứng (serial mainboard, serial hệ thống, MAC — machine-id luôn đổi
khi cài lại nên không xét): giống = đúng là cài lại; khác = có thể là máy khác. Nút **"Duyệt và thay thế"** (mức 3 trở lên,
`POST /admin/v1/devices/{id}/replace {"old_id"}`) trong một transaction: thu hồi máy cũ (chứng chỉ cũ hết dùng được), duyệt
máy mới, chuyển nhóm thử và cấu hình riêng theo máy (dịch vụ theo dõi, nhãn, bộ rule gán riêng), ghi `replaces_device`
(migration 0019) để chi tiết máy mới có nút "Xem lịch sử máy cũ". Nhật ký thao tác: `device.replace`, `device.revoke`.
Duyệt tự động không bao giờ tự thay thế. Duyệt thường máy trùng vẫn báo 409 như trước.

## Thông báo bản server mới (chỉ đề xuất)

- `scs-server-update-check.timer` chạy **10 phút sau khi bật máy rồi 6 giờ một lần**, gọi `/opt/scs-server/check-update.sh`:
  `git fetch` repo (chạy git **bằng user sở hữu repo**, không bằng root), chọn bản mới giống `update.sh` (tag `v*` mới
  nhất, chưa có tag thì `origin/main`), liệt kê các commit từ **bản đang chạy** (`scs-server version`) tới bản mới (tối đa
  60), ghi vào `/var/lib/scs-update/update-check.txt` (thư mục root:scs 750 — dịch vụ đọc được, không sửa được).
  **Không tự cài gì.**
- Tiến trình `scs-server` không gọi mạng ra ngoài và không đụng repo: chỉ đọc file đó (`SCS_UPDATE_CHECK_FILE`), API
  `GET /admin/v1/update` cho **quản trị gốc và mức 1**. Web hiện nhãn **"Có bản mới · N thay đổi"** trên thanh trên (mỗi bản
  báo một lần bằng thông báo góc màn hình) và thẻ **"Bản cập nhật SCS-Server"** ở trang **Hệ thống**: tiêu đề + nội dung
  từng thay đổi, bản mới có đổi cấu trúc DB không, lần kiểm cuối, lỗi nếu có. Quá 2 ngày chưa kiểm thì báo timer có vấn đề.
- Cài bản mới vẫn bằng menu **mục 4** / `scripts/update.sh` (test, sao lưu DB khi cần, lỗi thì quay về bản cũ). Cài xong
  `install.sh` tự chạy kiểm tra lại nên thông báo cũ biến mất. Kiểm ngay: `sudo systemctl start scs-server-update-check`.

## Lưu theo giai đoạn

Thiết kế: [docs/thiet-ke-luu-theo-giai-doan.md](../docs/thiet-ke-luu-theo-giai-doan.md). Không lưu từng sự kiện, từng heartbeat:

- Sự kiện `info` / `low` / `medium` cộng vào giai đoạn đang mở; `high` / `critical` đóng OK, mở **cảnh báo**.
- Cảnh báo high tự đóng sau `SCS_ALERT_AUTOCLOSE` (mặc định 24h) không có sự kiện nặng mới; đã lên critical thì chỉ
  admin đóng (web: **Đã xử lý**). Quá `SCS_OFFLINE_AFTER` (60m; agent gọi về ngẫu nhiên 5–30 phút, tính theo giây) không nghe máy: **mất liên lạc**, tự đóng khi máy gửi lại.
- Agent gửi sự kiện qua `POST /v1/logs` (lô đã ký, tối đa 500 sự kiện; high / critical sau vài giây, còn lại mỗi phút). Tiến trình `worker`
  (hoặc `all`) kiểm mỗi phút.
- **Xử lý sự cố** (trang Cảnh báo và dòng thời gian của máy, mức Xác nhận sự cố), bấm từng sự cố:
  - **Chờ thông tin** / **Đã gửi report**: sự cố **vẫn mở**, bắt buộc ghi chú (chờ ai / chờ gì, gửi cho ai, số phiếu); hiện
    nhãn trạng thái, lọc được trên trang Cảnh báo. Tự đóng vẫn như cũ: mức cao tự đóng khi yên đủ lâu, nghiêm trọng cần người đóng.
  - **Đã xử lý**: đóng sự cố (ghi chú tùy chọn). **Xác nhận sự cố**: đóng nhanh sự cố một lần (một bước xác nhận).
  - Mỗi lần đổi ghi lịch sử trong sự cố (≤ 50 mục, hiện ở dòng thời gian) và nhật ký thao tác (`period.handling`,
    `period.resolve`). Báo cáo sự cố tách cách đóng: đã xử lý / xác nhận sự cố / tự đóng / hệ thống; đếm sự cố đang mở chờ
    thông tin / đã gửi report. Migration 0021 (`handling*`, `resolution`; sự cố admin đóng trước đây tính là "đã xử lý").
- **Kế hoạch (chưa làm):** ghép nhiều sự cố thành một sự cố gộp (xử lý một lần cho cả nhóm), rồi rule phía server tự
  ghép / nâng mức / báo theo siêu thị, rule, số máy trong một khoảng thời gian — rule server không được đóng hay ẩn sự cố.
  Chi tiết: `docs/ke-hoach-giai-doan-1.html`.
- API web: `GET /admin/v1/devices/{id}/periods`, `POST /admin/v1/periods/{id}/resolve {note, resolution: fixed|confirmed}`,
  `POST /admin/v1/periods/{id}/handling {status: waiting_info|reported, note}`.
- Cảnh báo sự cố hiện luôn **lý do** (Tổng quan → "Cảnh báo mới" và trang Cảnh báo): tối đa 3 rule nặng nhất của giai
  đoạn, mỗi rule có tiêu đề (tra trong kho rule, đọc lại tối đa mỗi phút), mã rule, số lần, lần cuối và dòng log mẫu
  (tối đa 400 ký tự). Bấm vào cảnh báo mở chi tiết máy (tình trạng, dòng thời gian, thao tác).

## Trang Máy và khung Server

- **Thông tin siêu thị** (trang Máy → bấm mã siêu thị → **Sửa thông tin siêu thị**, cần mức 6): tên siêu thị, tổ chức,
  danh sách IP WAN (IP đầu là IP chính; máy gửi từ bất kỳ IP nào trong danh sách không bị cảnh báo "IP lạ"; tối đa 9),
  quản lý, phó quản lý, trưởng nhóm, phó phòng, ghi chú. Ô tên người và tổ chức gõ tự do, có gợi ý từ giá trị đã nhập ở
  siêu thị khác. Đầu nhóm hiện "mã - tên" và quản lý; ô tìm kiếm tìm cả tên, tổ chức, người phụ trách, IP; trang Cảnh báo
  hiện tên siêu thị. API `GET /admin/v1/stores` (Chỉ xem), `POST /admin/v1/stores/{mã}` (mức 6); mỗi lần sửa ghi `audit`
  (`store.update`). Migration 0008.
- **Nạp thông tin siêu thị từ file** (trang Máy → **Nạp thông tin từ file**; quyền **mức 2 "Duyệt và phát hành rule"
  trở lên**, giống nạp rule): file `.xlsx` hoặc `.csv` UTF-8 (dấu `,` `;` hoặc tab), tối đa 5 MB / 20.000 dòng, dòng đầu là
  tên cột.
  - **Ghép cột (mapping):** server tự đoán theo tên cột (tiếng Việt có dấu / không dấu, tiếng Anh: Store Code, Store
    Name, Organization, Ip, Manager, Deputy, Leader, Deputy Department, Note); bảng **Ghép cột** cho chọn lại từng cột
    (hoặc Bỏ qua), mỗi trường một cột, bắt buộc có cột mã siêu thị. API: `map=0=code,1=name,3=ips`.
  - **Bổ sung** (mặc định): ô trống giữ giá trị đang có. **Ghi đè** (`mode=overwrite`): ô trống của cột đã ghép XÓA giá trị
    đang có (dữ liệu giống y file); trường không ghép cột thì không đụng tới; web hỏi xác nhận trước khi ghi đè.
  - Luôn **xem trước** (thêm mới / thay đổi trường nào / không đổi / lỗi theo dòng) rồi mới ghi, trong một transaction,
    một dòng audit `store.import`; còn dòng lỗi thì không cho nạp. Server đọc file bằng thư viện chuẩn (`internal/tabular`,
    có giới hạn chống file nén độc). File `.xls` cũ chưa hỗ trợ (lưu lại thành `.xlsx`).
  - File mẫu: `GET /admin/v1/stores/template.csv`. Sửa từng siêu thị bằng form vẫn cần mức 6.
- **Trang Máy** gom máy theo mã siêu thị (hostname `<mã>-TN…`; máy chưa có mã nằm cuối). Viền trái tô theo máy tệ
  nhất trong siêu thị, tính từ cảnh báo đang mở: **đỏ** = cảnh báo nghiêm trọng (critical, phải có người đóng) hoặc
  mất liên lạc; **vàng** = cảnh báo khác đang mở (mức cao tự đóng sau 24 giờ, mức thấp, IP lạ, hostname sai...) hoặc
  có máy chờ duyệt; **xanh** = OK. Đỏ xếp trước, rồi vàng, rồi xanh. Bấm mã siêu thị để xổ danh sách máy (chấm màu
  từng máy); tìm theo hostname thì tự mở siêu thị chứa máy đó.
- **Tổng quan → khung Server** (chỉ xem, không có nút thao tác), tự cập nhật 10 giây: từng card mạng (IP, tốc độ
  nhận / gửi, lưu lượng từ 0 giờ), số kết nối agent đang mở; CPU, RAM, ổ chứa thư mục dữ liệu (thanh vàng từ 70%,
  đỏ từ 90%), thời gian máy chạy; heartbeat / phút, sự kiện log / phút, số máy gọi về trong 30 phút, RAM scs-server,
  dung lượng DB. API `GET /admin/v1/server/stats` (mức Chỉ xem). Server đọc `/proc` mỗi 10 giây trong RAM, không ghi
  DB; dung lượng DB hỏi tối đa mỗi phút một lần. Số heartbeat / kết nối agent chỉ có khi chạy `scs-server all`
  (cổng agent cùng tiến trình, cách menu cài); số đếm về 0 khi khởi động lại.

## Phát hành bản agent: lượt tải, băng thông, thống kê

- **Nạp bản agent không khởi động lại server**: scs-server kiểm thư mục `SCS_AGENT_DIST` (`/opt/scs-server/agent`) mỗi
  phút (chỉ so `VERSION` + kích thước, giờ sửa binary; đổi mới đọc lại) và nạp, ký bản mới vào kho. Bản agent build riêng
  bằng menu mục 19 / `scripts/release-agent.sh` (build trong `git worktree` tạm, không đổi code của server; đổi phần
  server / giao thức / DB thì cảnh báo, nên dùng mục 4). Cùng phiên bản mà nội dung khác vẫn giữ bản đã nạp.

- **Lượt tải** (`SCS_DOWNLOAD_SLOTS`, mặc định 4) áp cho **mọi lần tải**: bản agent, bộ rule, và chứng chỉ của **máy mới
  hòa mạng**. Heartbeat chỉ báo "có bản / bộ mới" cho máy giữ được lượt; máy khác chạy cái đang có, không báo lỗi, xin lại
  ở heartbeat sau (5–30 phút). Máy mới đã được duyệt mà hết lượt thì vẫn nhận "chờ duyệt", hỏi lại sau ~5 phút (chấp
  nhận hòa mạng chậm hơn). Máy đã cài đúng bộ rule đang gán thì không cần lượt. Lượt trả khi tải xong, hoặc tự thu lại sau 15 phút
  nếu máy không tải. Tải thẳng bản agent / bộ rule khi hết lượt bị trả 503; agent mới coi 503 là "chờ lượt", không
  phạt 1 giờ (agent cũ chờ 1 giờ). Chạy được với agent bản cũ vì server quyết định.
- **Băng thông** (`SCS_DOWNLOAD_MBPS`, mặc định 20 Mbit/s): tổng phần thân file tải (bản agent, bộ rule) của server;
  heartbeat, log, web không bị giới hạn. Ví dụ bản agent ~7,6 MB: 5.000 máy cần khoảng 4,2 giờ ở 20 Mbit/s.
- **Thống kê** (trang Agent, mức Chỉ xem; API `GET /admin/v1/agent/fleet`): số máy và % đã lên bản đang phát hành,
  biểu đồ tiến độ (bảng `rollout_progress`, 5 phút một mốc, giữ 90 ngày) kèm dự kiến lúc xong, lượt tải đang dùng và
  Mbit/s, số máy báo tải / kiểm chữ ký lỗi (agent báo qua heartbeat, tính từ lúc server khởi động), phân bố phiên bản,
  tình trạng bộ rule (đang quét / đã cài chưa quét / chưa có / agent cũ không báo), bảng theo trạng thái máy (mới chờ
  duyệt / đang hoạt động / mất liên lạc / từ chối, thu hồi × bản mới / chưa cập nhật / có rule / chưa có rule).
  Lượt tải và lỗi chỉ thấy khi web và cổng agent chạy chung tiến trình (lệnh `all`, cách cài mặc định).

## Trang Máy: lọc theo người phụ trách, ghép tài khoản

- Bộ lọc **Mã kho** (nhiều mã, cách nhau dấu phẩy, khớp đầu mã), **Quản lý**, **Trưởng nhóm** lọc tức thời theo thông tin
  siêu thị đã nạp, có gợi ý sẵn; gõ chữ thì khớp một phần, không phân biệt dấu; gõ đúng mã user (chỉ chữ số) thì khớp chính xác.
- Người phụ trách trong thông tin siêu thị ghi dạng **"mã user - Tên"** (ví dụ `100001 - Nguyễn Văn A`). Mã user trùng tên
  đăng nhập tài khoản web (đăng ký chỉ chữ số), nên web ghép được: trang **Tài khoản** (cả mục chờ duyệt) hiện tên thật và
  vai trò ở bao nhiêu siêu thị — giúp đối chiếu người đăng ký trước khi duyệt; bấm để mở trang Máy đã lọc. Tài khoản đăng
  nhập bằng email có ô **"Siêu thị của tôi"** (siêu thị mình là quản lý / phó / trưởng nhóm / phó phòng).
- Chỉ ghép trên web cho người đã đăng nhập; trang đăng ký (chưa đăng nhập) không lộ thông tin siêu thị.

## Báo cáo sự cố

Menu **Báo cáo sự cố** (ngay trên Báo cáo server, mức Chỉ xem). Tính các giai đoạn **cảnh báo** (sự cố từ log) và **mất
liên lạc** **bắt đầu** trong kỳ — chọn 24 giờ / 7 / 30 / 90 ngày hoặc khoảng ngày (tối đa 400 ngày, giờ của máy server):

- Tổng số, sự cố log / mất liên lạc, theo mức (nghiêm trọng / cao / trung bình), đang mở, cách đóng (admin xử lý / tự đóng
  / hệ thống), thời gian tới khi đóng (trung bình, trung vị, lâu nhất).
- Biểu đồ số sự cố bắt đầu mỗi giờ (kỳ ≤ 48 giờ) hoặc mỗi ngày; 10 siêu thị, 10 máy, 10 rule gặp nhiều nhất.
- Bảng chi tiết 20 sự cố mới nhất (bấm mở chi tiết máy); **Xuất Excel (.xlsx)** / **CSV** có đủ mọi sự cố (tối đa 20.000
  mới nhất): mã, loại, mức, bắt đầu / kết thúc, thời lượng, máy, siêu thị, số sự kiện, rule chính + tiêu đề, các rule,
  đóng bởi, ghi chú. CSV chặn chèn công thức (ô bắt đầu bằng `=`, `+`, `-`, `@`); Excel ghi chữ dạng chữ. **In / Lưu PDF**.
- API: `GET /admin/v1/incidents/report?from=&to=` (JSON), `.csv`, `.xlsx`. Chỉ mục `device_periods_incidents` (migration 0020).
- Không tính sức khỏe máy (chỉ để tham khảo) và các cảnh báo không có giai đoạn (IP lạ, chứng chỉ, máy chờ duyệt).

## Báo cáo server (lịch sử thông số)

Server ghi **mỗi phút một dòng** vào bảng `server_metrics` (migration 0009): CPU trung bình / đỉnh, tải, RAM, ổ đĩa
thư mục dữ liệu, lưu lượng mạng nhận / gửi (mọi card trừ card ảo Docker, loopback) và tốc độ đỉnh, heartbeat, sự kiện
log, số kết nối agent tối đa, RAM scs-server, dung lượng DB (hỏi 10 phút một lần). Giữ `SCS_METRICS_KEEP` (mặc định 90
ngày, xóa dòng cũ mỗi giờ; ~1.440 dòng / ngày). Chỉ ghi khi chạy web quản trị (`admin` / `all`); số heartbeat / kết nối
chỉ có khi `all`.

Web: trang **Báo cáo server** (thanh bên, hoặc nút **Báo cáo** trên khung Server ở Tổng quan; mức Chỉ xem): chọn 24 giờ /
7 / 30 / 90 ngày hoặc khoảng ngày, gom theo 10 phút / giờ / ngày (giờ của máy server). Có tổng kết (CPU, RAM trung bình và
đỉnh, ổ đĩa tăng bao nhiêu, tổng nhận / gửi, Mbit/s trung bình và đỉnh, heartbeat, dung lượng DB, % thời gian có số liệu
— phần thiếu là server tắt), biểu đồ CPU / RAM / băng thông / heartbeat, bảng chi tiết, nút **Xuất Excel (.xlsx)**
(ô số là số thật), **Xuất CSV** và **In / Lưu PDF** (bản in ẩn menu, nút). API: `GET /admin/v1/server/report`,
`report.xlsx`, `report.csv` (`from`, `to` = RFC 3339 hoặc YYYY-MM-DD, `step` = minute | 10min | hour | day; khoảng dài
tự tăng bước, tối đa 3.000 dòng).

## Thông báo tức thời trên web

`GET /admin/v1/stream` (Server-Sent Events, cùng cổng 7222, cùng phiên đăng nhập, không dùng dịch vụ ngoài). Khi có
trình duyệt đang mở, server hỏi DB 3 giây một lần (một câu SQL nhỏ) và đẩy số liệu khi có thay đổi: số cảnh báo, máy
chờ duyệt, mất liên lạc, bộ rule cần ký, các cảnh báo vừa mở / vừa nâng mức. Web cập nhật ngay, hiện cảnh báo mới ở góc
màn hình; nút chuông (thanh bên) bật thông báo hệ điều hành và tiếng báo cho mức cao / nghiêm trọng / mất liên lạc.
Tối đa 64 trình duyệt mở cùng lúc; phiên đăng xuất / bị thu hồi thì luồng bị ngắt trong 30 giây. Đóng hết tab web thì
không nhận được (không gửi ra ngoài).

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
- Khởi động lại server thì mọi người đăng nhập lại (phiên nằm trong RAM), mã đăng nhập SSH đổi và mã đã gửi qua
  mail (đăng nhập, xác minh đăng ký) mất hiệu lực; tài khoản vẫn còn trong DB.
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

Mở `https://<IP-server>:7222/` (qua VPN hoặc trên máy server). Trang đăng nhập có tab **Mã SSH** (mã 6 số lấy
bằng `scs-login-code` + tên của bạn) và, khi đã cấu hình gửi mail, tab **Email** và **Đăng ký** (mục "Tài khoản và mức quyền" bên dưới).

### Đăng nhập bằng mã 6 số (giống NASfone)

```bash
scs-server login-code          # hoặc: go run ./cmd/scs-server login-code
scs-server login-code -watch   # tự in mã mới mỗi phút, Ctrl+C để thoát
```

- Hai mã: **Admin** (= quản trị gốc, mức 0, toàn quyền) và **Chỉ xem** (mức 7, mọi thao tác ghi bị chặn 403). Hai mã không bao giờ trùng nhau.
- Mã đổi đầu mỗi phút, mã cũ còn dùng được thêm 10 giây. Mỗi mã dùng một lần; dùng mã Admin thì chỉ mã Admin đổi.
- Sai 5 lần thì đổi mã sớm. **Một IP sai 5 lần liên tiếp thì bị khóa 24 giờ** (xem "Khóa đăng nhập theo IP / thiết bị"
  bên dưới); toàn hệ thống 20 lần / 5 phút thì khóa đăng nhập
  5 phút, bị khóa lại liên tiếp thì gấp đôi (10, 20, 40, tối đa 60 phút), yên 1 giờ thì về 5 phút.
  Lượt thử được tính ngay khi request tới, nên gửi nhiều request cùng lúc cũng không vượt được giới hạn.
- Đang bị khóa thì `login-code` báo, kèm log mức error (có thể đang bị dò mã). Mở khóa ngay: `scs-server login-code -unlock`
  (gỡ luôn mọi khóa IP / thiết bị — dùng khi chính quản trị gốc bị khóa IP).
- Phiên là cookie HttpOnly, Secure, SameSite=Strict, không lưu lâu: tắt trình duyệt là mất, server cắt sau tối đa 24 giờ.
  Phiên nằm trong RAM: khởi động lại server thì mọi người đăng nhập lại.
- Lệnh `login-code` lấy mã từ server đang chạy qua Unix socket `SCS_CONTROL_SOCKET` (mặc định
  `data/run/control.sock`, socket 600 trong thư mục 700; thư mục có sẵn mà quyền rộng hơn thì server từ chối chạy):
  phải SSH vào được server bằng đúng user chạy dịch vụ mới đọc được mã.
- Không chạy web quản trị sau reverse proxy và không chuyển tiếp cổng 7222 trên máy server: danh sách
  `SCS_ADMIN_ALLOW` xét IP nguồn, mà mọi thứ chạy trên máy server đều mang IP 127.0.0.1. Request có header
  của proxy (`X-Forwarded-For`, `Forwarded`, ...) bị trả 403.
- Tên nhập lúc đăng nhập được ghi vào log (đăng nhập, đăng xuất, thu hồi phiên). Quản trị gốc xem và thu hồi phiên ở trang **Hệ thống**.
- `SCS_ADMIN_TOKEN` (tùy chọn) chỉ dành cho script gọi API bằng `Authorization: Bearer`, có quyền Admin.

### Tài khoản và mức quyền (đăng nhập bằng mã gửi qua email)

Ngoài mã lấy qua SSH, người dùng có thể đăng ký tài khoản bằng email (mặc định chỉ nhận `@gmail.com`,
`SCS_ACCOUNT_DOMAINS`). Không có mật khẩu: mỗi lần đăng nhập server gửi một mã 6 số về email đã đăng ký.

| Mức | Quyền | Thao tác trên web / API |
|---|---|---|
| 0 | Quản trị gốc | Chỉ khi đăng nhập bằng mã **Admin** lấy qua SSH (hoặc Bearer `SCS_ADMIN_TOKEN`). Duyệt tài khoản mọi mức, đặt / đổi mức quyền, khóa, xóa, giới hạn phiên; xem và thu hồi phiên |
| 1 | Duyệt cụm server | Duyệt / rút xác nhận quyết định cụm, duyệt server xin vào cụm, cô lập tại chỗ; **quản lý tài khoản mức 5–7** (duyệt, đổi mức, khóa, từ chối đăng ký) và gỡ khóa đăng nhập |
| 2 | Duyệt và phát hành rule | Thêm, ký duyệt, xóa bộ rule; gán bộ rule cho máy |
| 3 | Duyệt agent, phát hành bản cập nhật | Duyệt / từ chối / thu hồi máy; phát hành bản agent |
| 4 | Địa chỉ server gửi cho agent | Thêm / gỡ / sắp xếp địa chỉ server |
| 5 | Xác nhận sự cố | Xác nhận cảnh báo, đóng sự cố |
| 6 | Chỉnh thông tin agent | Cấu hình dịch vụ theo dõi, nhãn máy, nhóm thử |
| 7 | Chỉ xem | Chỉ đọc (mã **Chỉ xem** lấy qua SSH cũng là mức này) |

Số nhỏ hơn là quyền cao hơn, và mức trên làm được mọi việc của các mức dưới (mức 3 cũng xác nhận được sự cố).

- **Đăng ký:** tab **Đăng ký** → tên đăng nhập (**chỉ chữ số 0–9**, 3–32 số; không chữ cái, không ký tự đặc biệt) + email
  → mã xác minh gửi về email → nhập mã → tài khoản **chờ duyệt**.
  Quản trị gốc (chọn mức 1–7) hoặc người mức 1 (chọn mức 5–7) vào trang **Tài khoản** rồi **Duyệt** (người đăng ký nhận mail báo).
- **Đăng nhập:** tab **Email** → tên đăng nhập → **Gửi mã về email** → nhập mã.
- Mã hạn **5 phút** và **chỉ nhập được một lần**: nhập sai là mã bị hủy, phải xin mã mới. Mã chỉ nằm trong RAM
  (lưu dạng băm), không ghi vào DB hay log.
- **Khóa đăng nhập theo IP / thiết bị** (migration 0015, bảng `login_blocks`): nhập sai mã **5 lần liên tiếp cho cùng
  một tài khoản** (mã gửi qua email, mã xác minh đăng ký, cả mã SSH của quản trị gốc) → khóa IP đó **24 giờ**; nhập đúng
  thì đếm lại. Xin mã cho tên không có / không hoạt động, hoặc đăng ký sai định dạng, **quá 10 lần trong 24 giờ** → khóa
  IP **7 ngày**. IP của siêu thị (IP WAN đã nạp ở thông tin siêu thị, hoặc IP mà máy đã duyệt đang gửi về) nhiều người
  dùng chung nên **khóa theo mã thiết bị**: mỗi trình duyệt được cấp một cookie ngẫu nhiên `scs_dev` (HttpOnly, 1 năm,
  chỉ gửi cho `/auth`); xóa cookie để lách thì đụng trần — **quá 3 thiết bị bị khóa trong 24 giờ thì khóa cả IP 24 giờ**.
  Khi bị khóa chỉ chặn đăng nhập / đăng ký / xin mã, phiên đang mở vẫn dùng tiếp. Khóa lưu DB (khởi động lại vẫn còn),
  bộ đếm lần sai chỉ trong RAM. Gỡ khóa: trang **Tài khoản** → "Khóa đăng nhập" (quản trị gốc và mức 1), hoặc trên máy
  server `scs-login-code -unlock` (gỡ mọi khóa). Nhật ký thao tác ghi `login.block`, `login.unblock`.
  Giới hạn cũ vẫn giữ: mã SSH sai 20 lần / 5 phút trên toàn hệ thống thì khóa đăng nhập bằng mã SSH 5 → 60 phút.
- **Mức 1 quản lý tài khoản trong phạm vi mức 5–7**: thấy tài khoản chờ duyệt và tài khoản mức 5–7 (không thấy mức 1–4,
  không thấy chính mình); duyệt / đổi mức chỉ chọn được **5, 6, 7** (cao nhất là Xác nhận sự cố); khóa / mở khóa tài
  khoản 5–7; từ chối (xóa) tài khoản đang chờ; gỡ khóa đăng nhập. **Không** xóa tài khoản đang dùng, không đặt giới hạn
  phiên, không xem / thu hồi phiên, không đụng tới tài khoản mức 1–4, không tự sửa tài khoản của mình. Mọi kiểm tra ở
  server (403 kèm lý do, ghi log); giao diện chỉ ẩn bớt lựa chọn.
- Đổi mức, khóa hay xóa tài khoản thì mọi phiên đang mở của tài khoản đó bị cắt ngay.
- **Giới hạn phiên theo tài khoản** (chỉ quản trị gốc đặt, trang **Tài khoản**, cột "Phiên đăng nhập"; migration 0013):
  thời gian sống mỗi phiên **1 giờ – 15 ngày** (mặc định 24 giờ, tính từ lúc đăng nhập, không tự gia hạn khi dùng) và số phiên mở
  cùng lúc **1 – 20** (mặc định 4); đăng nhập thêm khi đã đủ thì **phiên cũ nhất bị cắt**. Đổi giới hạn áp ngay cho phiên
  đang mở (hạn = lúc đăng nhập + thời gian mới: nới thì kéo dài, rút thì phiên quá hạn mới hết; cắt phiên thừa cũ nhất) nhưng không đăng xuất phiên còn trong giới hạn — dùng cho BOT lấy dữ liệu
  cần phiên dài. Trang **Hệ thống** hiện thời gian còn sống của từng phiên; phiên đăng nhập bằng mã SSH vẫn 24 giờ.
  Nhật ký thao tác ghi `account.session_limits`.
- **Phiên gắn với máy và trình duyệt lúc đăng nhập** (IP + User-Agent): cookie bị đánh cắp đem dùng từ IP hay trình
  duyệt khác bị từ chối, phiên bị **thu hồi luôn** (người dùng thật phải đăng nhập lại) và server ghi log cảnh báo
  "phiên đăng nhập bị dùng từ máy / trình duyệt khác". Đổi IP giữa chừng (đổi mạng, VPN cấp IP khác) cũng phải đăng nhập lại.
  BOT lấy dữ liệu nên chạy từ IP cố định, giữ nguyên User-Agent.
- Chống dò và chống spam mail: server luôn trả cùng một câu dù tên / email có tồn tại hay không; mỗi tài khoản
  xin tối đa 3 mã / 15 phút, mỗi IP 10 mã / 15 phút và 20 lần nhập mã / 15 phút, đăng ký 5 lần / giờ / IP;
  tối đa 50 tài khoản chờ duyệt. Mail gửi ngầm (tối đa 4 mail cùng lúc).
- Cấu hình gửi mail: `SCS_SMTP_HOST/PORT/USER/PASSWORD/FROM` (bắt buộc STARTTLS, TLS 1.2+). Gmail:
  `smtp.gmail.com`, cổng `587`, nên dùng một Gmail riêng chỉ để gửi, bật xác minh 2 bước và tạo **Mật khẩu ứng dụng**.
  Không khai SMTP thì tab Email / Đăng ký bị ẩn, chỉ đăng nhập được bằng mã SSH. Trên server đã cài: menu **mục 17**
  điền Gmail và mật khẩu ứng dụng, gửi thư thử (`scs-server mail-test`), tắt / bật; cập nhật từ bản cũ tự thêm khối
  `SCS_SMTP_*` trống vào `server.env`.
- Bảng `accounts` (migration 0007): tên, email, trạng thái, mức quyền, ai duyệt, lần đăng nhập cuối. Mọi thay đổi
  ghi vào bảng `audit` (`account.register`, `account.update`, `account.delete`).

### Mã nguồn giao diện

Giao diện nằm trong
`internal/web/static/` (HTML/CSS/JS thuần, nhúng vào binary bằng `go:embed`, không cần Node).
Có CSP chặt: không script/style inline, không tải gì từ nguồn ngoài.

Mọi trang dùng dữ liệu thật từ API quản trị: Tổng quan, Máy, Cảnh báo, Báo cáo server, Chờ duyệt, Agent, Rule quét log,
Địa chỉ server, Cụm server, Tài khoản, Hệ thống. Chi tiết máy có thẻ **Tình trạng máy** (trạng thái agent gửi gần nhất).

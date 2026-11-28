# Ý tưởng giai đoạn 2: đội trưởng trong siêu thị

> Trạng thái (2026-11-28): **mới là ý tưởng, chưa chốt, chưa làm.** Ghi lại để suy nghĩ thêm. Phần khó nhất
> chưa có định hướng là ảnh hưởng tới **lưu theo giai đoạn** (mục 6) — thiết kế đó đang chạy tốt, không được làm
> hỏng nó.

## 1. Mục đích

Khi số máy lớn hơn, giảm số request về server: các agent trong một siêu thị giao tiếp với nhau qua LAN, chỉ
**một máy (đội trưởng)** gửi heartbeat gộp của cả siêu thị về server.

## 2. Ý tưởng ban đầu

- Mỗi siêu thị có **đội trưởng** và các **agent phụ**. Agent phụ gửi heartbeat cho đội trưởng qua LAN, đội trưởng
  gửi **một gói** chứa trạng thái của cả siêu thị (gói to hơn, nhưng mỗi siêu thị chỉ một request).
- Đội trưởng mất thì **chuyển cho máy tiếp theo**, cứ thế cho tới máy cuối cùng.
- Sau khi khởi động lại (reboot định kỳ): **máy nào gọi server trước** thì làm đội trưởng.
- Agent chưa biết đội trưởng thì gửi heartbeat thẳng lên server, server trả lời ai là đội trưởng của siêu thị.
- **Sự cố khẩn vẫn gửi thẳng** từ từng agent lên server như hiện nay.
- **Server quyết định giờ gửi** của từng siêu thị để rải đều request, tự giảm tải.

Môi trường thật (theo người vận hành): máy ở siêu thị **có bật ufw** (cần thử nghiệm), các máy trong một
siêu thị **cùng dải mạng**.

## 3. Góp ý thiết kế (đề xuất, chưa chốt)

### 3.1 Server làm trọng tài, quyền đội trưởng có hạn

- Chỉ **server** chỉ định đội trưởng. Agent không tự bầu nhau trong LAN (tự bầu dễ sinh hai đội trưởng khi mạng
  chập chờn hoặc hai máy khởi động gần nhau).
- Quyền đội trưởng có **thời hạn** (ví dụ 2 chu kỳ heartbeat), gia hạn bằng chính heartbeat gộp. Không gia hạn
  thì tự hết.
- Server gửi kèm **thứ tự kế nhiệm** (máy 2, máy 3…) nên các máy biết ai lên tiếp mà không phải bàn với nhau.

### 3.2 Chuyển giao khi đội trưởng chết

1. Agent phụ gửi cho đội trưởng; không được xác nhận (ví dụ 2 lần liên tiếp) thì **gửi thẳng lên server** như hiện nay.
2. Server thấy quyền đội trưởng hết hạn thì chỉ định máy kế tiếp, báo trong phản hồi heartbeat.
3. Đội trưởng cũ sống lại thì làm agent phụ, không tự giành lại.

Tính chất cần giữ: **tệ nhất là quay về đúng cách đang chạy hôm nay** (mỗi máy tự gửi).

### 3.3 Đội trưởng chỉ chuyển tiếp, không sửa được dữ liệu

- Mỗi agent phụ **ký báo cáo bằng khóa riêng của máy** (đã có). Đội trưởng chỉ gom và gửi.
- Server kiểm chữ ký từng báo cáo bên trong. Đội trưởng bị chiếm không giả được trạng thái máy khác — chỉ có thể
  *không gửi*, và máy bị bỏ sẽ tự gửi thẳng (3.2).
- Mỗi báo cáo có số thứ tự / thời gian để không gửi lại được báo cáo cũ; báo cáo trùng (vừa qua đội trưởng vừa gửi
  thẳng) phải được bỏ qua an toàn.
- Agent ↔ agent dùng mTLS bằng chứng chỉ do server cấp, chỉ nhận máy cùng CA và **cùng siêu thị** theo danh sách
  server gửi xuống (có chữ ký). Không tin máy nào chỉ vì nó trả lời trong LAN.

### 3.4 Server chỉ định giờ gửi

Làm được **riêng, trước**, không cần agent giao tiếp với nhau: server biết tổng số máy nên chia đều mốc gửi
thay cho ngẫu nhiên 5–30 phút. Khi có đội trưởng thì giao mốc theo siêu thị.

### 3.5 ufw và cài đặt

- Agent không tự mở được firewall (cần root). Trình cài thêm quy tắc **chỉ cho cùng dải mạng**:
  `ufw allow from <dải mạng của máy> to any port <cổng> proto tcp`. Không mở cổng ra ngoài.
- Máy đang chạy cần một bước một lần (kiểu `install.sh --smart-only`, ví dụ `--peer-only`). Máy chưa làm bước đó
  vẫn tự gửi như cũ.
- File unit hiện tại (`scs-agent.service`) đã cho phép `AF_INET` / `AF_INET6` và không giới hạn IP: mở cổng ≥ 1024
  **không cần sửa unit**, không cần root cho bản thân agent.

### 3.6 Chia sẻ bản cập nhật agent trong LAN

Có thể là **lợi lớn hơn** cả heartbeat: đợt phát hành gần nhất lên tới ~14 Mbit/s. Đội trưởng tải binary một lần,
chia cho máy cùng siêu thị qua LAN (máy nhận vẫn kiểm chữ ký phát hành như hiện nay). Băng thông WAN mỗi đợt giảm
theo số máy mỗi siêu thị.

### 3.7 Thứ tự làm đề xuất

1. Server chỉ định giờ gửi (rủi ro thấp, không cần LAN).
2. Chứng chỉ máy có `ServerAuth` + trình cài mở ufw đúng dải (thử trên máy ufw thật).
3. Đội trưởng + kế nhiệm + quay về gửi thẳng, **bật theo từng siêu thị** để thử vài nơi trước.
4. Chia sẻ bản cập nhật trong LAN.

## 4. Rủi ro

| # | Rủi ro | Hậu quả | Hướng giảm |
|---|---|---|---|
| R1 | Hai đội trưởng cùng lúc | Gửi trùng, tranh nhau | Chỉ server chỉ định, quyền có hạn (3.1); báo cáo trùng bỏ qua theo số thứ tự |
| R2 | Đội trưởng bị chiếm quyền | Giả trạng thái / giấu máy khác | Báo cáo ký bằng khóa từng máy; máy bị bỏ tự gửi thẳng (3.3) |
| R3 | Mở cổng trên máy cửa hàng | Thêm bề mặt tấn công (hiện agent **không mở cổng nào**) | Chỉ cùng dải mạng (ufw), mTLS bắt buộc, giới hạn tốc độ / kích thước gói, mặc định tắt |
| R4 | Đội trưởng chết, chuyển giao chậm | **Cả siêu thị báo mất liên lạc giả** | Thời gian chuyển giao + một mốc gửi phải nhỏ hơn hẳn `SCS_OFFLINE_AFTER` (60m) |
| R5 | ufw chặn / quy tắc sai | Agent không thấy nhau | Tự quay về gửi thẳng; thử trên máy thật trước |
| R6 | Giai đoạn chuyển tiếp: máy cũ + máy mới lẫn lộn trong một siêu thị | Có máy không tham gia được | Agent khai khả năng (`capabilities`); máy cũ tự gửi như hiện nay |
| R7 | Đồng hồ máy phụ lệch | Đặt sai giờ vào giai đoạn | Server dùng giờ của mình làm chuẩn, giờ máy chỉ để so (mục 6) |
| R8 | Lỗi logic ở phần mới làm sai dữ liệu giai đoạn đang chạy tốt | Mất tin cậy của Sức khỏe máy / Cảnh báo | Giữ nguyên đường ghi theo từng máy (mục 6, hướng A) |
| R9 | Độ phức tạp tăng nhiều (bầu, kế nhiệm, LAN, chứng chỉ, ufw) | Khó gỡ lỗi khi có nhiều siêu thị | Công tắc theo siêu thị, log rõ vai trò, trang web hiện ai là đội trưởng |

## 5. Thiếu sót và việc còn tồn đọng (phải giải trước khi làm)

- **Chứng chỉ máy chỉ có `ClientAuth`** (`server/internal/pki/pki.go`): agent nhận kết nối cần `ServerAuth`. Hướng:
  server cấp thêm `ServerAuth`, máy nhận ở lần gia hạn chứng chỉ kế tiếp (cơ chế renew đã có).
- **Server phải cập nhật cùng agent**: danh sách máy cùng siêu thị (có chữ ký), chỉ định đội trưởng, mốc gửi,
  nhận và mở gói gộp.
- **Cách tính mất liên lạc** phải đổi từ "máy không gửi" sang "không có báo cáo mới của máy, dù qua đường nào" (mục 6).
- **Số liệu thật để kỳ vọng đúng:** request heartbeat giảm theo **số máy trung bình mỗi siêu thị** (vài máy → vài
  lần), không phải 10 lần. Số lần ghi DB gần như không giảm (server vẫn xử lý báo cáo từng máy); giảm là số kết nối
  TLS và request. Tải hiện rất nhẹ (700 máy: 34 heartbeat / phút, CPU 2–3%) — việc này **chưa gấp**.
- **Sự kiện low / medium** hiện gửi `POST /v1/logs` mỗi phút từ từng máy: qua đội trưởng hay giữ gửi thẳng? Chưa quyết.
- **Kiểm kê LAN**: đội trưởng có thể gánh luôn việc kiểm (đỡ kiểm trùng) hay giữ mọi máy cùng kiểm? Chưa quyết.
- **Web**: cần hiện ai là đội trưởng, máy nào đang gửi thẳng, lý do.

## 6. Ảnh hưởng tới lưu theo giai đoạn (chỗ chưa có định hướng)

Thiết kế hiện tại (`docs/thiet-ke-luu-theo-giai-doan.md`) đang chạy tốt: mỗi máy đúng một giai đoạn mở; mỗi
heartbeat là một `UPDATE device_status`, chỉ ghi `device_status_changes` khi đổi đáng kể; mất liên lạc khi quá
`SCS_OFFLINE_AFTER` không nghe máy gửi gì.

Câu hỏi: khi heartbeat đi qua đội trưởng, **"nghe tin từ máy" nghĩa là gì**, và giai đoạn có phải đổi không?

### Hướng A — giữ nguyên, đội trưởng chỉ là đường vận chuyển (đề xuất để bắt đầu)

Server mở gói gộp, **xử lý từng báo cáo bên trong như một heartbeat riêng** của máy đó, đi đúng đường ghi hiện
nay. Bảng, giai đoạn, Sức khỏe máy, Cảnh báo **không đổi**.

- Lợi: không đụng vào phần đang tốt; máy gửi thẳng hay qua đội trưởng đều ra cùng kết quả.
- Có thể gom các `UPDATE` của cả siêu thị vào **một transaction** → ít lần commit hơn (lợi thêm cho DB).
- Phải chốt: giờ dùng để kéo dài giai đoạn là **giờ server nhận gói** hay **giờ đội trưởng thấy máy lần cuối**
  (đội trưởng báo "thấy máy X cách đây N giây"). Giờ nhận gói đơn giản; giờ "thấy lần cuối" đúng hơn khi máy phụ
  chết giữa hai lần gửi.
- Phải chốt: máy có trong gói nhưng **"không thấy từ lâu"** thì không tính là đã nghe tin → để hạn mất liên lạc
  chạy bình thường.

### Hướng B — thêm lớp siêu thị bên trên (không thay lớp máy)

Giữ nguyên giai đoạn theo máy, thêm một **dòng thời gian theo siêu thị** suy ra từ các máy: đủ máy / thiếu máy /
mất cả siêu thị. Cả siêu thị mất WAN hôm nay sinh N cảnh báo mất liên lạc riêng lẻ; lớp này gộp thành một sự cố
"mất liên lạc cả siêu thị" (gần với ý "Ghép sự cố" ở kế hoạch giai đoạn 2).

- Lợi: dễ đọc khi số máy lớn.
- Đánh đổi: thêm bảng và luật suy ra; làm sau khi hướng A chạy ổn.

### Hướng C — chỉ lưu theo siêu thị (không khuyên)

Coi siêu thị là đơn vị, bỏ giai đoạn theo máy. Mất lịch sử từng máy, mâu thuẫn với Sức khỏe máy / SMART / thay
máy. Ghi lại để loại trừ.

### Điểm phải giữ dù chọn hướng nào

- Mỗi máy vẫn **đúng một giai đoạn mở**, dòng thời gian không lỗ.
- **Mất liên lạc không được che cảnh báo đang mở** (như hiện nay).
- Đội trưởng chết **không được** làm cả siêu thị hiện mất liên lạc giả (R4).
- Báo cáo trùng (qua đội trưởng + gửi thẳng) không được kéo dài / đổi giai đoạn hai lần.

## 7. Câu hỏi còn mở

1. Giờ kéo dài giai đoạn: giờ server nhận gói hay giờ đội trưởng thấy máy lần cuối? (mục 6, hướng A)
2. Sự kiện low / medium đi qua đội trưởng hay giữ gửi thẳng?
3. Đội trưởng gánh kiểm kê LAN hay mọi máy cùng kiểm?
4. Hạn quyền đội trưởng bao nhiêu chu kỳ, bao nhiêu lần không xác nhận thì máy phụ gửi thẳng?
5. Có làm lớp siêu thị (hướng B) không, và khi nào?
6. Cổng dùng cho agent ↔ agent, và thử ufw ở siêu thị nào trước?

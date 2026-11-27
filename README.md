# SCS-System

**SCS = Spider Control Scanning.** SCS-Server như con nhện ở trung tâm: các chân vươn tới từng siêu thị, nơi mỗi máy
chạy SCS-Agent liên tục **quét** log và trạng thái; có sự cố ở đâu thì server biết ngay và điều khiển (duyệt máy, phát
hành rule, bản cập nhật) từ một chỗ.

Hệ thống giám sát máy Ubuntu đặt tại nhiều điểm bán: **SCS-Agent** chạy trên từng máy, gửi trạng thái
và sự kiện log về **SCS-Server**; admin duyệt máy và theo dõi trên web quản trị.

*A monitoring system for fleets of Ubuntu machines across many sites: SCS-Agent reports machine status
and matched log events to SCS-Server, where admins approve machines and watch alerts on a web console.
Code comments and docs are in Vietnamese.*

> Trạng thái: giai đoạn 1, **đang chạy thật** từ 11/2026 (khoảng 400 máy, xem [số đo thực tế](#số-đo-thực-tế)).
> Vẫn đang phát triển tiếp.
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
| [`docs/`](docs/) | [Kế hoạch giai đoạn 1](docs/ke-hoach-giai-doan-1.html), [hướng dẫn viết rule](docs/huong-dan-viet-rule.md) (kèm kho rule, duyệt và ký), [thiết kế cụm nhiều server](docs/thiet-ke-cum-server.md), [lưu theo giai đoạn](docs/thiet-ke-luu-theo-giai-doan.md) |

## Nguyên tắc thiết kế

- **Agent chỉ chủ động gọi lên server**; server không gửi lệnh xuống. Rule và bản cập nhật agent tự kéo về, kiểm chữ ký.
- **Web quản trị phân 8 mức quyền** (0 quản trị gốc … 7 chỉ xem): quản trị gốc chỉ có khi lấy mã bằng SSH trên máy
  server; người khác đăng ký tài khoản, đăng nhập bằng **app xác thực** (Google / Microsoft Authenticator, không cần
  mạng ra ngoài) hoặc mã gửi về mail, được quản trị gốc duyệt và đặt mức.
- **Mọi máy phải được duyệt** trước khi gửi được dữ liệu: admin duyệt tay, hoặc **duyệt tự động** (công tắc, mặc định
  tắt) chỉ khi máy đúng hostname, thuộc siêu thị đã có thông tin và gửi từ IP WAN của siêu thị đó. Không có bí mật dùng
  chung trong source agent.
- Mỗi máy có **khóa Ed25519 riêng** (không rời máy): ký mọi gói tin và dùng làm chứng chỉ mTLS do CA nội bộ của server cấp.
- Gói tin có giờ và nonce (chống gửi lại), giao tiếp qua TLS 1.3, agent chỉ tin CA của server.
- **Khóa đăng nhập theo IP**: sai mã 5 lần liên tiếp cho một tài khoản → khóa IP 24 giờ; dò tên / đăng ký sai quá 10
  lần trong 24 giờ → khóa 7 ngày. IP của siêu thị khóa theo từng trình duyệt (cookie mã thiết bị) để không chặn cả
  siêu thị; quá 3 trình duyệt bị khóa trong 24 giờ thì khóa cả IP.
- **Phiên web gắn với IP + trình duyệt lúc đăng nhập**: cookie bị đánh cắp đem sang máy / trình duyệt khác bị từ chối
  và phiên bị thu hồi. Có test tự động cho leo thang quyền (7 mức × 26 route quản trị) và dùng lại phiên đánh cắp.
- Dữ liệu lưu trong **PostgreSQL + TimescaleDB**, ràng buộc nằm trong DB. Sự kiện và trạng thái máy lưu **theo giai đoạn**
  (OK / cảnh báo / mất liên lạc, mỗi giai đoạn một dòng) thay vì mỗi sự kiện một dòng; giữ 60 ngày sau khi giai đoạn đóng
  ([thiết kế](docs/thiet-ke-luu-theo-giai-doan.md)).
- **Rule chỉ đọc log và khớp regex** (RE2), không chạy được lệnh trên máy; bộ rule phải có chữ ký.

## Cổng

| Cổng (TCP) | Ai gọi | Dùng cho |
|---|---|---|
| 6222 | agent → server | đăng ký, heartbeat, tải bản agent và bộ rule, gửi sự kiện |
| 7222 | trình duyệt admin → server | web quản trị, chỉ mở cho các dải IP được phép (mở cả Internet được nếu chủ động xác nhận, xem INSTALL §1.3) |
| 5222 | server ↔ server | liên lạc trong cụm; chỉ mở khi server đã thuộc cụm hoặc đang xin vào; chỉ nhận khóa server có trong danh sách cụm |

Không dùng UDP. Phía máy agent không phải mở cổng nào.

## Hiện trạng chức năng (bản một server)

| Phần | Trạng thái |
|---|---|
| Đăng ký máy, admin duyệt, chứng chỉ mTLS, heartbeat, nhiều địa chỉ server (thêm / gỡ khi đổi mạng) | Đã có |
| Agent tự cập nhật từ server (bản ký, nhóm thử / tất cả, tự quay về), thay cả bản khung | Đã có |
| Cấu hình agent và nhãn máy 3 cấp (chung / siêu thị / máy) trên web | Đã có |
| Rule quét log: khung rule, schema 2 (`tags`, `applies_to`), **kho rule trên server**: tự chia chưa ký / đã ký / cần ký lại / lỗi, duyệt (ký) trên web và menu mục 16, báo bộ cần ký ở Tổng quan và đầu menu | Đã có |
| **Lưu theo giai đoạn**: trạng thái máy (chỉ ghi khi đổi đáng kể), cảnh báo high tự đóng sau 24 giờ, critical chỉ admin đóng, mất liên lạc sau 60 phút (agent gọi về ngẫu nhiên 5–30 phút một lần, tính theo giây) tự đóng khi máy gửi lại; dòng thời gian từng máy trên web; cổng nhận sự kiện `POST /v1/logs` | Đã có phía server |
| **Thông báo tức thời trên web** (không dùng dịch vụ ngoài): số trên thanh bên và trang đang xem tự cập nhật, cảnh báo mới hiện góc màn hình; bật nút chuông thì thêm thông báo hệ điều hành và tiếng báo cho mức cao / nghiêm trọng / mất liên lạc. Cần mở ít nhất một tab web | Đã có |
| **Bộ rule đã áp dụng trên từng máy**: agent báo bộ đang cài (tên, phiên bản, SHA-256, chữ ký đúng / lỗi, số rule chạy trên máy) trong heartbeat; web hiện ở trang Máy, chi tiết máy, và số máy đang dùng mỗi bộ ở trang Rule. Cần agent bản mới | Đã có |
| **Tài khoản web quản trị bằng Gmail và 8 mức quyền**: đăng ký → mã xác minh về mail → quản trị gốc (mã SSH) duyệt và đặt mức 1–7 (cụm server, rule, agent + bản cập nhật, địa chỉ server, xác nhận sự cố, thông tin agent, chỉ xem); đăng nhập bằng mã gửi về mail (5 phút, nhập một lần); trang **Tài khoản**; tên đăng ký chỉ chữ số; quản trị gốc đặt thời gian sống phiên (1 giờ – 15 ngày, mặc định 24 giờ) và số phiên cùng lúc (1 – 20, mặc định 4) cho từng tài khoản. Cần Gmail gửi mã (`SCS_SMTP_*`) | Đã có, đã lên server thật |
| **Đăng nhập bằng app xác thực (TOTP)** khi mạng chặn SMTP: đăng ký không cần email → duyệt → quản trị **Tạo mã cài app** (8 số, 15 phút, một lần) → người dùng nhập mã, quét QR (tạo trên server, không thư viện ngoài), xác nhận mã đầu tiên; đăng nhập bằng mã 6 số trong Google / Microsoft Authenticator; **Đặt lại app** khi mất điện thoại. Khóa lưu DB mã hóa AES-256-GCM bằng file `SCS_TOTP_KEY` (cập nhật mục 4 tự tạo), mã đã dùng không dùng lại được (migration 0022) | Đã có, chưa chạy thật |
| **Trang Máy gom theo mã siêu thị**, viền màu theo máy tệ nhất: đỏ = cần xử lý (cảnh báo nghiêm trọng, mất liên lạc), vàng = đang chờ xử lý (cảnh báo khác đang mở, máy chờ duyệt), xanh = OK; bấm mã siêu thị để xổ danh sách máy. **Bộ lọc mã kho / quản lý / trưởng nhóm** lọc tức thời theo thông tin siêu thị đã nạp (gợi ý sẵn, nhiều mã kho cách nhau dấu phẩy, gõ không dấu vẫn khớp; gõ đúng mã user thì khớp chính xác), ô "Siêu thị của tôi". **Ghép tài khoản với thông tin siêu thị**: người phụ trách ghi dạng "mã user - Tên", mã user trùng tên đăng nhập (chữ số) → trang Tài khoản (cả mục chờ duyệt) hiện tên thật và vai trò ở bao nhiêu siêu thị, bấm mở trang Máy đã lọc. **Tổng quan có khung Server** (chỉ xem): tốc độ / lưu lượng mạng, CPU, RAM, ổ đĩa, heartbeat và sự kiện mỗi phút, kết nối agent, dung lượng DB | Đã có |
| **Thông tin siêu thị** trên web: tên, tổ chức, nhiều IP WAN, quản lý / phó quản lý / trưởng nhóm / phó phòng, ghi chú; tìm kiếm theo các trường này. **Cảnh báo hiện lý do** (rule, dòng log), bấm mở tình trạng máy | Đã có, đã lên server thật |
| **Xử lý sự cố từng bước**: trên trang Cảnh báo và dòng thời gian máy, mỗi sự cố chọn **Chờ thông tin** / **Đã gửi report** (vẫn mở, bắt buộc ghi chú, có nhãn và ô lọc) hoặc đóng bằng **Đã xử lý** / **Xác nhận sự cố** (đóng nhanh sự cố một lần); tự đóng theo mức như cũ (cao tự đóng, nghiêm trọng cần người đóng); lịch sử xử lý trong sự cố + nhật ký thao tác (migration 0021) | Đã có, chưa chạy thật |
| **Báo cáo sự cố** (menu ngay trên Báo cáo server, mức Chỉ xem): sự cố log và mất liên lạc bắt đầu trong kỳ (24 giờ / 7 / 30 / 90 ngày hoặc khoảng ngày) — số theo giờ / ngày, theo mức, đang mở, cách đóng (đã xử lý / xác nhận sự cố / tự đóng / hệ thống), đang mở chờ thông tin / đã gửi report, thời gian tới khi đóng (trung bình, trung vị, lâu nhất), siêu thị / máy / rule gặp nhiều nhất; bảng 20 sự cố mới nhất (bấm mở máy); xuất Excel / CSV đủ mọi sự cố (tối đa 20.000), in / lưu PDF. Sức khỏe máy không tính | Đã có, chưa chạy thật |
| **Báo cáo server**: lưu thông số mỗi phút (CPU, RAM, ổ đĩa, băng thông, heartbeat, DB) 90 ngày; trang báo cáo có biểu đồ, bảng 20 dòng mới nhất (đủ số liệu trong file xuất), xuất Excel / CSV, in PDF. **Nạp thông tin siêu thị từ file .xlsx / .csv** có xem trước | Đã có, đang chạy thật |
| **Duyệt tự động** máy mới (công tắc ở trang Chờ duyệt, mặc định tắt, mức Agent): đủ 4 điều kiện (hostname đúng quy ước, siêu thị có trong dữ liệu đã nạp, gửi từ IP WAN của siêu thị, không trùng máy đang hoạt động) thì duyệt ngay; thiếu thì chờ người duyệt, lý do hiện trên thẻ máy; nạp thông tin xong thì lần hỏi lại kế tiếp được duyệt | Đã có, đã lên server thật |
| **Giảm tải server**: agent gọi heartbeat ngẫu nhiên 5–30 phút (theo giây, server đặt khoảng bằng `next_poll_max_s`); mất liên lạc sau 60 phút (`SCS_OFFLINE_AFTER`, cập nhật server tự đổi 30m cũ sang 60m). Agent bản cũ vẫn 5–10 phút tới khi cập nhật | Đã có, đang chạy thật |
| **Dời heartbeat đầu sau khi máy khởi động** (lịch reboot cùng giờ mỗi sáng): máy đã duyệt chờ ngẫu nhiên 5–30 phút (theo giây) mới gọi về, cắt để khoảng trống từ heartbeat cuối trước khi tắt không quá 45 phút; restart giữa chừng / lần chạy đầu vẫn gọi ngay; quét log không bị dời. Bản khung mới được 90 phút (trước 30) để gọi được server trước khi tự quay về. Cần agent bản mới | Đã có, chưa chạy thật |
| **Phân lượt mọi lần tải**: tối đa 4 máy tải cùng lúc (`SCS_DOWNLOAD_SLOTS`) — bản agent, bộ rule, cả chứng chỉ máy mới hòa mạng — tổng ≤ 20 Mbit/s (`SCS_DOWNLOAD_MBPS`); máy chưa tới lượt chạy cái đang có (máy mới: vẫn chờ duyệt), xin lại ở lần gọi sau. **Trang Agent có thống kê**: số máy / % đã lên bản mới, biểu đồ tiến độ kèm dự kiến xong, lượt tải đang dùng, máy báo lỗi tải, phân bố phiên bản, tình trạng bộ rule, bảng theo trạng thái máy | Đã có, đang chạy thật |
| **Sức khỏe máy (A2)**: agent gửi trạng thái máy trong mỗi heartbeat (RAM, swap, tải, nhiệt độ, dung lượng + inode từng phân vùng, phân vùng chỉ đọc, ổ vật lý + lỗi I/O, RAID, NTP, dịch vụ) — không cần thêm quyền. Server liệt kê **ghi nhận để tham khảo** (không chấm mức) — **tách khỏi cảnh báo** (không mở cảnh báo, không tô viền siêu thị). Web: menu riêng **Sức khỏe máy** (nhóm Tham khảo: đếm máy có / không có ghi nhận, lọc theo loại ghi nhận, bấm mở chi tiết máy), thẻ "Tình trạng máy" trong chi tiết máy, cột Sức khỏe và ô lọc ở trang Máy. Chưa đọc SMART. Cần agent bản mới | Đã có, chưa chạy thật |
| **Agent quét log liên tục** theo bộ rule (file trong /var/log nhớ vị trí, nhận ra xoay vòng; journald qua `journalctl`), hàng đợi trên đĩa khi mất mạng, gửi sự kiện: high / critical sau vài giây, còn lại mỗi phút | Đã có (A3), đang chạy thật |
| **Server phát bộ rule**: gán bộ đã ký cho tất cả máy / siêu thị / máy trên trang Rule; agent nhận qua heartbeat, tự tải, kiểm chữ ký, cài rồi quét theo bộ mới | Đã có (M7), đang chạy thật |
| **Gia hạn chứng chỉ máy** (hạn 90 ngày): agent tự gia hạn khi còn dưới 30 ngày bằng cách gửi lại gói đăng ký với chính khóa máy (chứng chỉ đã hết hạn vẫn gia hạn được); qua lượt tải; web hiện số máy sắp / đã hết hạn và hạn từng máy. **Cần phát hành bản agent mới cho mọi máy trước khi chứng chỉ đầu tiên hết hạn** — agent bản cũ không tự gia hạn | Đang chạy thật (cấp lại 90 ngày, web hiện hạn từng máy) |
| **Sao lưu DB định kỳ** ngày 1 và 16 hằng tháng (≈ 15 ngày, giữ 6 bản), sao lưu tay và hướng dẫn khôi phục ở menu mục 18; mỗi lần cập nhật tự chạy test SQL trên DB riêng `scs_test` trước khi cài | Đã có; kiểm SQL khi cập nhật đã chạy thật, lịch sao lưu chờ lần chạy đầu (01/12) |
| **Thông báo bản server mới** cho quản trị gốc và mức 1 (chỉ đề xuất, không tự cài): timer kiểm GitHub 6 giờ / lần, web hiện "Có bản mới" và danh sách thay đổi từ bản đang chạy; cài vẫn bằng menu mục 4. **Sửa lỗi**: nới thời gian sống phiên giờ kéo dài cả phiên đang mở (trước chỉ rút ngắn, phiên cũ vẫn hết sau 24 giờ) | Đã có, chưa chạy thật |
| **Cập nhật server tự kiểm SQL trên PostgreSQL + TimescaleDB thật** (DB riêng `scs_test`) trước khi cài; lỗi thì không cài, giữ bản cũ. Lần chạy đầu bắt được lỗ hổng ràng buộc DB (hostname TN1/TN2 thiếu hậu tố lọt qua khi vai trò NULL, sửa ở migration 0014) và deadlock khi xóa hypertable (bộ chạy migration nay tự chạy lại) | Đã có |
| **Duyệt và thay thế máy cũ** khi cài lại máy: máy chờ duyệt trùng hostname / vị trí TN với máy đang hoạt động thì thẻ chờ duyệt hiện "Cài lại máy", so sánh phần cứng (serial mainboard / hệ thống, MAC); nút "Duyệt và thay thế" thu hồi máy cũ + duyệt máy mới trong một thao tác, chuyển nhóm thử và cấu hình riêng (dịch vụ, nhãn, bộ rule), chi tiết máy mới mở được "lịch sử máy cũ" (migration 0019). Mức 3 trở lên; duyệt tự động không tự thay thế | Đang chạy thật |
| **SMART ổ cứng** (NVMe + SATA, không cần smartmontools): tác vụ root riêng `scs-agent-smart.timer` mỗi ngày, chỉ giữ CAP_SYS_RAWIO + CAP_SYS_ADMIN, mở ổ chỉ đọc, không mạng; agent chỉ đọc file kết quả. Ghi nhận tham khảo: ổ tự báo hỏng, SSD dùng ≥ 90% tuổi thọ, sector đã thay / chờ thay, lỗi media NVMe, lỗi CRC (cáp). Ổ ảo của card RAID phần cứng / ổ USB / ổ SAS ghi lý do không đọc được. Máy đang chạy cần chạy bộ cài agent một lần với `--smart-only` (menu agent mục 9). Trang **Sức khỏe máy** có thẻ **SMART ổ cứng**: đếm và lọc máy đọc được / chưa cài tác vụ / agent cũ / kết quả cũ hơn 3 ngày / không đọc được ổ, kèm cột SMART; chi tiết máy nói rõ vì sao chưa có SMART | Đã có, chưa chạy thật |
| **Kiểm thiết bị trong LAN theo rule** (rule nguồn `probe`, schema 3): agent ping (ICMP, không cần thêm quyền) hoặc mở kết nối TCP tới router (`{gateway}`), máy in, đầu ghi… **chỉ khi bộ rule đã ký yêu cầu**; kết quả thành dòng `probe icmp <ip> down …` cho rule khớp / đếm / báo như log. Giới hạn: chỉ IPv4 nội bộ cố định, ≤ 16 đích cả bộ, nhịp ≥ 60 giây (server kiểm lúc ký, agent kiểm lại); server không phát bộ schema 3 cho agent cũ; máy tắt bằng `SCS_PROBES=off` | Đã có, chưa chạy thật (cần phát hành bản agent mới) |
| **Thiết bị LAN — kiểm kê, chỉ tham khảo** (sơ đồ `lan:` trong bộ rule, schema 4): bộ rule đã ký kèm danh sách thiết bị cố định của siêu thị (cân, máy thu ngân, máy in, đầu ghi…, tối đa 48). **Mọi máy có agent** kiểm sau mỗi 7 heartbeat thành công (~2 giờ; ping không cần thêm quyền hoặc mở cổng TCP, thử lại 1 lần), gửi bitmap kèm heartbeat kế tiếp (~128 byte). Agent **chỉ kiểm thiết bị cùng dải mạng với máy**; server **chỉ ký sơ đồ có IP trong `SCS_LAN_ALLOW`** (mặc định trống = không ký; khai bằng menu mục 21). Server gộp các máy theo siêu thị: online (có máy thấy trong 5 giờ), chưa từng thấy (siêu thị không có, không tính thiếu), vắng, lâu không thấy (> 30 ngày), lệch giữa các máy, máy báo bất thường, không rõ; **đối chiếu máy thu ngân quét được với máy đã có agent** (khớp / chưa có agent / chưa rõ / sai IP). Web: menu riêng **Thiết bị LAN** (nhóm Tham khảo, **tự cập nhật** khi dữ liệu đổi, ≤ 10 giây): chấm màu từng thiết bị, đếm có mặt / từng thấy, lọc theo loại, xuất CSV, chi tiết siêu thị + lịch sử 10 ngày (migration 0023). Không cảnh báo, không mở sự cố. Cụm nhiều server: mỗi server chỉ thấy máy gửi về nó | Đã có, chưa chạy thật (cần phát hành bản agent mới, rồi mới gán bộ có sơ đồ) |
| **Giảm độ trễ thông báo**: agent chỉ gửi gấp (~1 giây) sự kiện làm máy chuyển trạng thái, cùng mức thì gom mỗi phút (server trả mức cảnh báo đang mở); server đánh thức luồng thông báo ngay khi ghi, trạng thái đổi đẩy ngay, số đếm gom mỗi phút; trình duyệt vẽ ngay cảnh báo mới / nâng mức; file giao diện có ETag + gzip, JSON API quản trị nén gzip. Sự cố nặng lên web sau ~1–2 giây | Đã có, chưa chạy thật (phần agent cần phát hành bản mới) |
| **Nạp bản agent không khởi động lại server** (menu mục 19, `scripts/release-agent.sh`): build agent trong thư mục tạm, test, chép vào `/opt/scs-server/agent`; server tự kiểm thư mục mỗi phút và nạp, ký vào kho phát hành — phiên web và kết nối agent giữ nguyên. Đổi phần server / giao thức / DB thì cảnh báo, dùng mục 4 | Đã có, chưa chạy thật |
| **Khởi động lại server từ menu** (mục 22): nạp lại server.env sau khi sửa tay, chờ server lên rồi in log gần nhất; phiên web nằm trong RAM nên mọi người đăng nhập lại, agent tự gọi lại ở heartbeat sau | Đã có, chưa chạy thật |
| **Chép nguyên server sang máy khác qua SSH** (menu mục 20, `scripts/move-server.sh`): DB, CA, khóa phát hành, cấu hình, dữ liệu; giữ CA nên agent không phải cài lại; dừng và tắt tự chạy server cũ, kiểm SHA-256 hai đầu, xóa bản tạm bằng `shred` | Đã có, chưa chạy thật |
| **Server kiểm chứng chỉ máy**: còn dưới 14 ngày mà chưa gia hạn → cảnh báo "Chứng chỉ máy sắp hết hạn" (cao), hết hạn → "đã hết hạn" (nghiêm trọng), tự đóng khi gia hạn xong (migration 0018). Agent tự xin lại chứng chỉ khi server không nhận chứng chỉ còn hạn (tối đa mỗi giờ); lệnh `scs-agent renew` gia hạn tay; `scs-agent status` hiện hạn chứng chỉ | Đã có, đang chạy (cảnh báo chỉ hiện khi có máy chạm ngưỡng 14 ngày) |
| Kiểm nonce chống gửi lại gói (R3): server nhớ nonce trong cửa sổ hiệu lực của gói (±5 phút) và từ chối gói trùng (409) — heartbeat, lô sự kiện, đăng ký / gia hạn; chỉ ghi sau khi kiểm chữ ký; nhớ trong RAM (khởi động lại thì quên) | Đã có (không cần cập nhật agent) |
| **Khóa đăng nhập theo IP / thiết bị** (sai mã 5 lần → 24 giờ; sai tên quá 10 lần / 24 giờ → 7 ngày; IP siêu thị khóa theo trình duyệt, quá 3 trình duyệt → khóa IP), lưu DB, gỡ trên web hoặc `scs-login-code -unlock`. **Mức 1 quản lý tài khoản mức 5–7** (duyệt, đổi mức, khóa, từ chối đăng ký), không đụng mức 1–4 | Đang chạy thật, đã kiểm đúng thiết kế |
| Tăng cứng thêm web quản trị khi mở ra Internet: chứng chỉ tự ký; mã SSH vẫn còn khóa chung toàn hệ thống (20 lần sai / 5 phút) | Chưa có |

Đã chạy trên Ubuntu + PostgreSQL / TimescaleDB thật (khoảng 400 máy): migration 0001–0020, heartbeat ngẫu nhiên, quét log
và phát bộ rule, phân lượt tải (bản agent và bộ rule đúng 4 lượt, ≤ 20 Mbit/s), báo cáo server, cập nhật server có kiểm SQL
trên `scs_test` (đã chặn đúng hai lỗi trước khi cài), **gia hạn chứng chỉ máy 90 ngày**, **duyệt và thay thế máy cũ**,
**khóa đăng nhập theo IP / thiết bị**. Chưa kiểm thật: khôi phục DB từ bản sao lưu, chép server sang máy khác (menu mục 20),
cụm nhiều server, các phần agent mới (SMART, kiểm thiết bị theo rule, gửi gấp theo trạng thái, kiểm kê thiết bị LAN — cần
phát hành bản agent), đăng nhập bằng app xác thực (migration 0022 trên PostgreSQL thật, quét QR bằng điện thoại thật),
trang Thiết bị LAN và tự cập nhật trang LAN / sức khỏe (migration 0023, 0024 trên PostgreSQL thật).

## Số đo thực tế

Đo trên server thật ngày 20/11/2026, khoảng **400 máy** (398 máy đã duyệt, 363 máy gọi về trong 30 phút gần nhất),
agent gọi heartbeat ngẫu nhiên 5–30 phút; số liệu báo cáo server 24 giờ:

| Chỉ số | Khoảng 400 máy (20/11) | Khoảng 150 máy (19/11) | Ghi chú |
|---|---|---|---|
| Heartbeat | 16–20 lần / phút (~25.800 / ngày) | 5–15 lần / phút | khớp tính toán: 400 máy ÷ 17,5 phút trung bình ≈ 23 lần / phút |
| CPU máy server | trung bình 3%, đỉnh 37,7% (vài lần ngắn) | 1–5% | máy 4 lõi |
| RAM `scs-server` | 37–42 MB | 25–35 MB | RAM cả máy ~19% của 7,3 GB, gần như phẳng |
| Dung lượng DB | 16,9 MB (tăng ~1,3 MB / ngày) | 13 MB | nhờ lưu theo giai đoạn (chỉ ghi khi có thay đổi) |
| Băng thông | gửi trung bình 0,74 Mbit/s, đỉnh 7,3; nhận đỉnh 15 Mbit/s | — | đỉnh lúc phát bản agent / bộ rule (phân lượt ≤ 20 Mbit/s) |
| Kết nối agent cùng lúc | tối đa 2 | — | mỗi agent chỉ mở vài giây khi gọi về |
| Sự kiện log | ~1.550 / ngày (~0,9 / phút) | — | |

Tải tăng gần tuyến tính theo số heartbeat như ước lượng; RAM server gần như không đổi theo số máy.

## Định hướng tương lai (chưa triển khai)

**Agent cho Windows**: ưu tiên Windows 10/11 IoT Enterprise LTSC (máy POS, kiosk) và Windows 10/11 64-bit; Windows Server
2016+ nếu có; không hỗ trợ Windows 7/8.1. Dùng lại phần lõi (giao thức, đăng ký, chứng chỉ, bộ máy rule, hàng đợi);
phải làm mới: đọc Windows Event Log (rule khớp theo kênh + Event ID + provider, không theo câu chữ), bật Audit Policy,
chạy dạng Windows Service, tự cập nhật kiểu Windows, trạng thái máy qua WMI, bộ cài MSI, ký mã Authenticode, quy ước
hostname trong giới hạn 15 ký tự của NetBIOS. Bắt đầu bằng kiểm kê máy Windows tại siêu thị (vai trò, phiên bản, số
lượng, có vào domain / Intune không).

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

Trên server đã cài, dùng **menu server mục 12–14** (trạng thái cụm, tạo cụm, xin vào cụm) thay cho gõ lệnh.
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

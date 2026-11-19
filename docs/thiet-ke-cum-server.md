# Thiết kế cụm SCS-Server (nhiều server)

Trạng thái: **bản thiết kế đã chốt (2026-11-12)**. Mốc **C1 đã có**: khóa node, chứng chỉ tự ký, nhật ký quyết định (quyết định tạo cụm), cổng 5222, lệnh `scs-server cluster init | status | ping`. Mốc **C3 đã có**: xin vào cụm qua mạng (`cluster join`), admin xác nhận trên từng node, cô lập tại chỗ / toàn cụm, bỏ cô lập, gỡ node, trang web "Cụm server". Mốc **C2 đã có phần lõi**: đề nghị, bỏ phiếu theo round có bằng chứng, gom chữ ký quá nửa, phát và bắt kịp nhật ký qua 5222, phát hiện ký hai lần và tách nhánh (test với cụm 3, 4, 5 node). **Chưa có**: đưa duyệt / thu hồi máy và cấu hình agent vào nhật ký (C2 phần hai, làm cùng C4), agent theo cụm (C4), trạng thái chung và web gom dữ liệu (C5), phát hành bằng chữ ký nhiều node (C6). Tài liệu này ghi các quyết định đã chốt và cách làm chi tiết, để xem lại trước khi bắt đầu.

Mục lục

1. [Mục tiêu và quyết định đã chốt](#1-mục-tiêu-và-quyết-định-đã-chốt)
2. [Thuật ngữ](#2-thuật-ngữ)
3. [Kiến trúc: ba loại dữ liệu, ba cổng](#3-kiến-trúc-ba-loại-dữ-liệu-ba-cổng)
4. [Danh tính: khóa riêng của server và agent](#4-danh-tính-khóa-riêng-của-server-và-agent)
5. [Nhật ký quyết định và quy tắc quá nửa](#5-nhật-ký-quyết-định-và-quy-tắc-quá-nửa)
6. [Danh sách cụm: thêm, cô lập, gỡ server](#6-danh-sách-cụm-thêm-cô-lập-gỡ-server)
7. [Trạng thái chung (siêu thị, máy)](#7-trạng-thái-chung-siêu-thị-máy)
8. [Dữ liệu lớn và web quản trị gom dữ liệu](#8-dữ-liệu-lớn-và-web-quản-trị-gom-dữ-liệu)
9. [Agent trong cụm](#9-agent-trong-cụm)
10. [Phát hành bản agent và bộ rule](#10-phát-hành-bản-agent-và-bộ-rule)
11. [Mạng: LAN, Internet, NAT, DDNS, VPN](#11-mạng-lan-internet-nat-ddns-vpn)
12. [Mất kết nối giữa các server (chia cụm)](#12-mất-kết-nối-giữa-các-server-chia-cụm)
13. [Chuyển từ một server lên cụm](#13-chuyển-từ-một-server-lên-cụm)
14. [Mối đe dọa và cách chặn](#14-mối-đe-dọa-và-cách-chặn)
15. [Thứ tự làm](#15-thứ-tự-làm)
16. [Đã xác nhận](#16-đã-xác-nhận-2026-11-12)
17. [Dữ liệu siêu thị và tài khoản giữa nhiều server (mốc D1)](#17-dữ-liệu-siêu-thị-và-tài-khoản-giữa-nhiều-server-mốc-d1)

---

## 1. Mục tiêu và quyết định đã chốt

Chạy nhiều SCS-Server thành một **mạng lưới ngang hàng**: server nào cũng là gốc, không có server chính / phụ. Agent gửi dữ liệu về một server, giữ server đó, lỗi mới chuyển kênh.

| # | Quyết định |
|---|---|
| Q1 | Dữ liệu agent gửi về server nào thì **server đó giữ**, không đồng bộ. Web quản trị xem được dữ liệu ở mọi server. **Trạng thái siêu thị chia sẻ chung.** |
| Q2 | Máy được duyệt trên một server thì **được cả mạng lưới nhận**. Liên lạc giữa server phải chống server lạ và kẻ đứng giữa. |
| Q3 | **Đồng thuận theo quy tắc quá nửa**: một quyết định chỉ có hiệu lực khi đủ chữ ký của quá nửa số server **đang hoạt động và hợp lệ**. Riêng **thêm server cần 100%**. |
| Q4 | **Bỏ CA.** Agent và server tin nhau qua **danh sách cụm có chữ ký** của các server. |
| Q5 | **Thêm server phải được mọi server hợp lệ duyệt** (điều kiện bắt buộc để hòa mạng). Có **cô lập** (khẩn cấp) và **gỡ** server, theo quá nửa. |
| Q6 | Web quản trị chạy trên mọi server, thay đổi đi qua đồng thuận. |
| Q7 | Server liên lạc với nhau qua **cổng riêng 5222**. |
| Q8 | Agent giữ một server, nhưng **mỗi 12 giờ hỏi một server khác bất kỳ, chọn ngẫu nhiên** về danh sách cụm (để thoát khỏi server bị hack). |
| Q9 | **Bản agent phải có chữ ký của quá nửa số server, mỗi chữ ký chỉ có sau khi admin xác nhận trên chính server đó.** Không còn một khóa phát hành duy nhất. |
| Q10 | Hỗ trợ mọi kiểu mạng: cùng LAN, khác VLAN, Internet (IP WAN tĩnh, chuyển tiếp cổng / NAT, DDNS), VPN (WireGuard, Tailscale). |
| Q11 | Lần đầu agent gọi một server; server trả về **danh sách server của máy đó theo quy tắc gán server** (đặt trên web). |
| Q12 | **Làm cụm ngay**, trước các mốc còn lại của bản một server. |

## 2. Thuật ngữ

| Từ | Nghĩa |
|---|---|
| **Node** | một SCS-Server trong cụm |
| **Khóa node** | khóa Ed25519 riêng của mỗi server, sinh lúc cài, không bao giờ rời máy |
| **Mã node** | 16 ký tự đầu của SHA-256 khóa công khai, dùng để gọi tên node |
| **Danh sách cụm** | các node đang có: mã node, tên, khóa công khai, địa chỉ, trạng thái (hoạt động / cô lập) |
| **Quyết định** | một thay đổi chung của cụm (duyệt máy, đổi cấu hình, thêm server…) |
| **Nhật ký quyết định** | chuỗi quyết định nối nhau bằng mã băm, mỗi quyết định có chữ ký của nhiều node |
| **Node hợp lệ** | node có trong danh sách cụm, đang hoạt động (không bị cô lập, không bị gỡ) |
| **Quá nửa** | ⌊N/2⌋ + 1, với N = số node **hợp lệ trong danh sách cụm** |
| **Quy tắc gán server** | quyết định cho biết nhóm máy nào dùng những node nào, theo thứ tự nào |

## 3. Kiến trúc: ba loại dữ liệu, ba cổng

```
                ┌──────────── cổng 5222: node ↔ node (chỉ khóa node trong danh sách cụm) ───────────┐
                │                                                                                    │
        ┌───────┴───────┐                 ┌───────────────┐                 ┌───────────────┐        │
        │    Node A     │ ◀── 5222 ──▶    │    Node B     │ ◀── 5222 ──▶    │    Node C     │ ◀──────┘
        │ DB riêng A    │                 │ DB riêng B    │                 │ DB riêng C    │
        │ web 7222      │                 │ web 7222      │                 │ web 7222      │
        └───────▲───────┘                 └───────▲───────┘                 └───────▲───────┘
                │ 6222                            │ 6222                            │ 6222
           agent máy 1                       agent máy 2                       agent máy 3
```

| Loại | Gồm | Cách xử lý |
|---|---|---|
| **A. Quyết định chung** | danh sách cụm; duyệt / từ chối / thu hồi máy, mã siêu thị; cấu hình agent; bộ rule; bản agent; nhóm phát hành | **nhật ký quyết định**, quá nửa node ký ([mục 5](#5-nhật-ký-quyết-định-và-quy-tắc-quá-nửa)). Mọi node giữ bản đầy đủ |
| **B. Trạng thái chung** | máy nào, lần cuối thấy lúc nào, đang gửi về node nào; siêu thị online / offline; cảnh báo đang mở | node nào thấy thì **node đó ký và phát** cho các node khác ([mục 7](#7-trạng-thái-chung-siêu-thị-máy)). Không cần đồng thuận |
| **C. Dữ liệu lớn** | heartbeat chi tiết, trạng thái máy theo thời gian, sự kiện rule, log | **node nhận giữ**, không đồng bộ. Web hỏi trực tiếp node đó ([mục 8](#8-dữ-liệu-lớn-và-web-quản-trị-gom-dữ-liệu)) |

| Cổng | Ai gọi | Bảo vệ |
|---|---|---|
| **6222** | agent → node | TLS 1.3; agent chỉ tin khóa node có trong danh sách cụm; node nhận ra agent bằng khóa máy đã duyệt |
| **7222** | trình duyệt admin → node | như hiện nay: chỉ dải IP được phép, mã đăng nhập 6 số của chính node đó |
| **5222** | node ↔ node | TLS 1.3 hai chiều; mỗi bên chỉ nhận khóa node có trong danh sách cụm. Riêng yêu cầu xin vào cụm được đi vào mà không cần khóa có sẵn, nhưng bị giới hạn tần suất và phải được mọi node duyệt |

Mỗi node có **PostgreSQL riêng**. NATS vẫn dùng nội bộ trong từng node (hàng đợi log ở mốc M5), không dùng giữa các node.

## 4. Danh tính: khóa riêng của server và agent

### Node

- Node sinh **khóa node Ed25519** lần đầu chạy lệnh cụm (`SCS_CLUSTER_DIR/node.key`, mặc định `/var/lib/scs-server/data/cluster/node.key`, quyền 600, user `scs`).
- Chứng chỉ TLS của node là **chứng chỉ tự ký từ chính khóa node**, trên cả 3 cổng. Không cần CA.
- Khi nối tới một node khác (5222) hay khi agent nối tới node (6222), bên gọi **so khóa công khai trong chứng chỉ với danh sách cụm**. Không có trong danh sách hoặc đang bị cô lập thì ngắt kết nối. Không so IP hay tên miền, nên đi qua LAN, NAT, VPN hay đổi IP đều không ảnh hưởng.
- Kẻ đứng giữa không có khóa node thì không giả được node, dù chặn được đường truyền.
- Nhật ký quyết định lưu ở `SCS_CLUSTER_DIR/log.jsonl` (mặc định `/var/lib/scs-server/data/cluster/`; file chỉ ghi thêm, mỗi quyết định một dòng, mở lại là kiểm lại toàn bộ chữ ký). Mỗi quyết định đều có chữ ký, nên node mất file vẫn tải lại được từ node khác và tự kiểm, không cần tin node đưa.

### Agent

- Giữ nguyên như hiện nay: máy tự sinh khóa Ed25519 và mọi gói tin đều ký bằng khóa đó.
- **Không còn chứng chỉ máy do CA cấp.** Agent dùng chứng chỉ tự ký từ khóa máy. Node nhận ra agent bằng cách so khóa với **danh sách máy đã duyệt trong nhật ký quyết định**. Mọi node có cùng danh sách, nên agent chuyển sang node nào cũng được nhận ngay.
- Thu hồi máy là một quyết định. Có hiệu lực là mọi node từ chối máy đó.

### Vì sao bỏ CA

Nếu một CA chung được chép lên mọi node, hack một node là lấy được khóa CA và cấp được chứng chỉ server giả mà mọi agent tin. Danh sách cụm có chữ ký của quá nửa node thì không có khóa "chủ" nào để lấy: hack một node chỉ có khóa của node đó, và khóa này bị loại khi node bị cô lập.

## 5. Nhật ký quyết định và quy tắc quá nửa

### Cấu trúc một quyết định

```jsonc
{
  "seq": 128,                         // số thứ tự, tăng dần, không có lỗ
  "round": 1,                         // vòng thử (tăng khi hai đề nghị tranh cùng seq)
  "prev": "sha256 của quyết định 127",
  "type": "device.approve",
  "payload": { "device_id": 42, "public_key": "...", "store_code": "12345" },
  "proposer": "mã node đề nghị",
  "admin": "người bấm trên web (tên phiên đăng nhập)",
  "created": "2026-11-12T08:00:00Z",
  "nonce": "16 byte ngẫu nhiên",
  "sigs": { "mã node A": "chữ ký", "mã node C": "chữ ký" }
}
```

Các node ký trên toàn bộ nội dung (trừ `sigs`). Ngữ cảnh ký riêng `scs-cluster/v1`, nên chữ ký này không dùng thay được cho chữ ký heartbeat, bộ rule hay bản agent.

### Quy trình

```
admin bấm trên web của node A
   │
   ▼
A tạo đề nghị (seq = cuối + 1, round 1), tự kiểm, ký, gửi cho mọi node qua 5222
   │
   ▼
mỗi node nhận tự kiểm:
  - nội dung hợp lệ (máy tồn tại, đúng định dạng…)
  - seq đúng nối tiếp nhật ký của nó, prev đúng
  - nó CHƯA ký đề nghị nào khác cho cùng (seq, round)
  → đạt thì ký và gửi chữ ký về A (và các node khác)
   │
   ▼
đủ quá nửa chữ ký → quyết định có hiệu lực → A phát bản đủ chữ ký cho mọi node
   │
   ▼
mỗi node kiểm lại đủ chữ ký hợp lệ rồi mới áp dụng vào DB của nó
```

- **Mỗi node chỉ ký một đề nghị cho mỗi (seq, round).** Quá nửa của cùng một tập node, nên không thể có hai quyết định khác nhau cùng đủ chữ ký cho một seq.
- Hai node đề nghị cùng lúc mà không bên nào đủ quá nửa (ví dụ chia phiếu 2–2 trên 4 node): node đề nghị gom các phiếu có chữ ký của round đó làm **bằng chứng rằng không đề nghị nào còn có thể đủ phiếu**, kể cả khi mọi node chưa bỏ phiếu đều dồn cho nó. Có bằng chứng thì các node mới ký ở round sau. Không có bằng chứng thì không ai được sang round sau, nên một đề nghị đã đủ chữ ký ở round trước không bao giờ bị đè.
- Node đã ký ở round sau thì không ký round trước nữa. Phiếu đã bỏ được ghi xuống đĩa **trước khi** trả chữ ký, nên node khởi động lại cũng không ký trùng.
- **Node không tin node đề nghị.** Node nào cũng tự kiểm đủ chữ ký trước khi áp dụng, nên một node bị hack không ghi được quyết định bậy.
- **Node chậm hay vừa bật lại** hỏi các node khác những quyết định còn thiếu, kiểm chữ ký từng quyết định rồi áp dụng. Không cần tin node đưa dữ liệu.
- Mỗi quyết định tính quá nửa theo **danh sách cụm tại thời điểm ngay trước nó**.

### Mức chữ ký

| Loại quyết định | Cần chữ ký |
|---|---|
| Duyệt / từ chối / thu hồi máy, đổi mã siêu thị, cấu hình agent, nhóm phát hành, bộ rule | **quá nửa** node đang hoạt động |
| Phát hành bản agent | **quá nửa**, và mỗi chữ ký chỉ có sau khi admin xác nhận trên chính node đó ([mục 10](#10-phát-hành-bản-agent-và-bộ-rule)) |
| Quy tắc gán server | **quá nửa** |
| Cô lập node X trên toàn cụm | **quá nửa** node hợp lệ, không tính X |
| Bỏ cô lập node X | **quá nửa**, mỗi chữ ký cần admin xác nhận trên chính node đó |
| Gỡ hẳn node X | **quá nửa** node hợp lệ, không tính X |
| **Thêm node** | **100% node hợp lệ**, mỗi node do admin duyệt trên chính node đó |
| Đổi địa chỉ của node X | chữ ký của X + quá nửa |

### "Còn sống và hợp lệ" tính thế nào

Quá nửa tính trên **số node hợp lệ trong danh sách cụm**, không tính trên số node *đang trả lời được lúc đó*. Đây là điều bắt buộc:

- Nếu đếm theo node đang trả lời, khi mạng giữa hai địa điểm đứt, **mỗi bên đều thấy mình là quá nửa của những node còn liên lạc được**, và hai bên ra hai quyết định trái nhau (ví dụ bên này duyệt, bên kia thu hồi cùng một máy). Không gộp lại được.
- Đếm theo danh sách cụm thì luôn chỉ có tối đa một bên đủ quá nửa.

Một node **chết hẳn** (hỏng máy, không bật lại được) vẫn nằm trong mẫu số cho tới khi bị cô lập hay gỡ. Cách xử lý: các node còn lại **cô lập** node chết (quá nửa), từ đó nó không còn được tính, rồi gỡ hẳn khi chắc chắn không dùng nữa. Ví dụ cụm 5 node chết 1: cô lập cần 3 trong 4 node còn lại; sau đó quá nửa là 3/4, thêm node mới cần cả 4/4.

Cụm **từ 3 node** chịu được 1 node tắt; 5 node chịu được 2. Cụm 2 node vẫn chạy được, nhưng quá nửa của 2 là 2: tắt một node là không ra được quyết định mới (agent vẫn gửi dữ liệu bình thường).

## 6. Danh sách cụm: thêm, cô lập, gỡ server

Danh sách cụm **không phải file riêng**: nó được dựng lại từ các quyết định `member.*` trong nhật ký. Mỗi bản danh sách có **số đời** (epoch) tăng dần và mã băm của quyết định tạo ra nó.

### Mọi thay đổi danh sách cụm cần admin xác nhận trên từng node

Một node **chỉ ký** quyết định thêm / cô lập / bỏ cô lập / gỡ node khi **admin đã xác nhận việc đó trên chính node ấy** (trang web "Cụm server", đăng nhập bằng mã của node đó). Nhờ vậy một node bị chiếm không thể tự cô lập hay gỡ node khác, hay kéo node lạ vào cụm, chỉ bằng cách gửi đề nghị: các node chưa có xác nhận tại chỗ sẽ không ký.

- Xác nhận có hạn 7 ngày; rút lại được khi quyết định chưa thành.
- Node hỏi trước các node khác đã được xác nhận chưa rồi mới bỏ phiếu. Chưa đủ thì không bỏ phiếu, để không giữ chỗ làm chậm các quyết định khác; web báo rõ còn chờ node nào.
- Quyết định về danh sách cụm có **dạng duy nhất**: cùng một việc (ví dụ "cô lập node X") thì node nào dựng cũng ra đúng một bản, nên khi admin xác nhận trên nhiều node, chữ ký cộng dồn vào một quyết định thay vì chia phiếu cho nhiều bản. Vì thế quyết định loại này không ghi tên admin hay giờ bấm; việc ai xác nhận lúc nào nằm trong log của từng node.
- Server tự thử lại mỗi 30 giây các việc đã được xác nhận mà chưa đủ node.

### Tạo cụm (node đầu tiên)

```
sudo -u scs /opt/scs-server/scs-server cluster init -cluster scs -name hn-01 -addr https://<IP>:5222 -env-file /etc/scs-server/server.env
```

Node đầu tiên tự ký quyết định `member.genesis` (cụm 1 node, quá nửa = 1). Mã băm của quyết định này là **mã cụm**, in ra màn hình và đóng vào gói cài agent.

### Thêm node

```
Node mới D:  scs-server cluster join https://<địa chỉ node A>:5222 -fingerprint <mã khóa của A> -name ... -addr ...
   │           (D so mã khóa của A trước khi gửi: sai là dừng; in mã khóa của D để admin đối chiếu)
   │           rồi khởi động lại scs-server trên D để mở cổng cụm
   ▼
A nhận "xin vào cụm" (cổng 5222, giới hạn tần suất, tối đa 20 yêu cầu chờ), báo cho mọi node
   │
   ▼
Admin đăng nhập web TỪNG node đang hoạt động (A, B, C) → trang "Server chờ vào cụm"
   → đối chiếu mã khóa D với mã in trên D → bấm Duyệt
   (mỗi node ký bằng khóa của nó; đăng nhập bằng mã của chính node đó)
   │
   ▼
đủ chữ ký của MỌI node → quyết định member.add có hiệu lực
   │
   ▼
D (tự hỏi A mỗi 10 giây) tải toàn bộ nhật ký, kiểm chữ ký từ quyết định đầu tiên → hòa mạng
   │
   ▼
heartbeat kế tiếp: mọi agent nhận danh sách cụm mới có D
```

Thêm node cần **100% node hợp lệ**. Một node đang tắt thì không thêm được node mới: chờ node đó bật lại, hoặc cô lập nó trước (quá nửa), rồi mới thêm.

### Cô lập node (khi nghi bị tấn công)

Có hai mức, làm được cùng lúc:

1. **Cô lập tại chỗ (ngay lập tức, không cần đồng thuận):** admin bấm "Cô lập X" trên node A. A ngừng nhận kết nối 5222 từ X, ngừng ký đề nghị của X, không tính chữ ký của X nữa. Chỉ làm giảm niềm tin nên không cần ai đồng ý và không thể bị lợi dụng. Làm trên các node còn lại để cô lập X ở cả cụm ngay.
2. **Cô lập toàn cụm (quyết định `member.isolate`, quá nửa node không tính X):** X bị đánh dấu cô lập trong danh sách cụm. **Agent nhận danh sách mới thì không gửi dữ liệu cho X nữa** và chuyển sang node khác. X không còn được tính vào quá nửa.

Bỏ cô lập (`member.restore`) cần quá nửa node, và mỗi chữ ký cần admin xác nhận trên chính node đó, vì đây là cho node đó lấy lại quyền.

### Gỡ node

`member.remove`: **quá nửa** node hợp lệ trừ X ký. Khóa của X bị xóa khỏi danh sách cụm vĩnh viễn; muốn quay lại thì X phải sinh khóa mới và xin vào cụm từ đầu. Dữ liệu lớn (loại C) nằm trên X không còn xem được qua web; nếu cần giữ lại thì sao lưu DB của X trước khi gỡ.

## 7. Trạng thái chung (siêu thị, máy)

Không đưa vào nhật ký quyết định: thay đổi quá nhiều (mỗi máy 5 phút một lần) và không cần đồng thuận, vì mỗi node chỉ khai những gì chính nó thấy.

- **60 giây một lần**, mỗi node gửi cho mọi node khác một **bản tóm tắt có chữ ký**:
  ```json
  { "node": "mã node B", "seq": 9012, "at": "...",
    "devices": [ { "id": 42, "last_seen": "...", "agent_version": "...", "alerts": ["DISK-IO-ERR-01"] } ] }
  ```
  chỉ gồm các máy thay đổi từ bản trước, kèm một bản đầy đủ mỗi giờ.
- Mỗi node lưu, với từng máy, **bản mới nhất theo `last_seen`** trong các bản tóm tắt nhận được.
- **Máy online** nếu có node nào thấy máy trong 60 phút gần nhất (`SCS_OFFLINE_AFTER`, 2 nhịp heartbeat 5–30 phút). **Máy offline** khi **không node nào** thấy. Máy đã chuyển kênh thì node mới thấy nó, nên không bị báo offline oan.
- **Trạng thái siêu thị** là tổng hợp từ các máy của siêu thị đó, tính tại mỗi node từ cùng dữ liệu, nên mọi web hiện giống nhau.
- **Chống giả:** bản tóm tắt phải có chữ ký của node gửi, node đó đang hoạt động, `seq` tăng dần. Một node bị hack chỉ khai sai được về **các máy đang gửi về nó**; cô lập node đó thì dữ liệu của nó bị bỏ.

## 8. Dữ liệu lớn và web quản trị gom dữ liệu

- Dữ liệu lớn lưu tại node nhận, giữ 60 ngày như hiện nay. **Không đồng bộ.**
- Web chạy trên mọi node. Khi admin xem một máy hay một siêu thị, node đang mở web **gửi câu hỏi tới mọi node** qua 5222 (chỉ đọc), chờ tối đa 5 giây, rồi **ghép kết quả theo thời gian**.
- **Node không trả lời** thì web ghi rõ "Node B không trả lời, thiếu dữ liệu từ B", không lặng lẽ hiện thiếu.
- Máy chuyển kênh nên lịch sử của nó nằm ở nhiều node; trạng thái chung ([mục 7](#7-trạng-thái-chung-siêu-thị-máy)) cho biết máy từng gửi về những node nào, để chỉ hỏi đúng các node đó.
- Câu hỏi giữa các node có chữ ký của node hỏi, kèm tên người dùng và mức quyền (0 quản trị gốc … 7 chỉ xem, xem server/README.md "Tài khoản và mức quyền"); node được hỏi ghi nhật ký thao tác.
- **Thay đổi** (duyệt máy, đổi cấu hình…) trên web là **đề nghị** gửi vào nhật ký quyết định ([mục 5](#5-nhật-ký-quyết-định-và-quy-tắc-quá-nửa)). Web hiện trạng thái "đang chờ đủ chữ ký (2/3)".
- Phiên đăng nhập và mã đăng nhập vẫn riêng từng node. Tài khoản email (bảng `accounts`) và thông tin siêu thị hiện
  riêng từng node; thiết kế đồng bộ ở [mục 17](#17-dữ-liệu-siêu-thị-và-tài-khoản-giữa-nhiều-server-mốc-d1) (chưa làm).

## 9. Agent trong cụm

### Gói cài

Gói cài agent mang **mã cụm** và **danh sách cụm mới nhất kèm chuỗi quyết định `member.*`** (thay cho `agent-ca.pem`), cùng địa chỉ của mọi node. Agent kiểm chuỗi từ quyết định đầu tiên nên không thể bị đưa danh sách giả.

### Quy tắc gán server

Admin đặt trên web (quyết định `assign.set`, quá nửa) **nhóm máy nào dùng những node nào, theo thứ tự nào**. Ví dụ:

| Điều kiện (khớp từ trên xuống) | Danh sách node | Thứ tự |
|---|---|---|
| mã siêu thị trong khoảng 10001–10500 | hn-01, hn-02, hcm-01 | cố định (ưu tiên hn-01) |
| hostname khớp mẫu `*-TN1-PRI` | hn-02, hn-01 | cố định |
| máy trong nhóm thử | test-01 | cố định |
| **mặc định** (không khớp quy tắc nào) | mọi node hợp lệ | **trộn theo từng máy** (chia tải đều) |

- Node bị cô lập hay gỡ tự bị bỏ khỏi mọi danh sách.
- Danh sách của một máy do node tính, gửi xuống trong mỗi heartbeat.
- "Trộn theo từng máy" dùng mã máy làm hạt giống: một máy luôn nhận cùng thứ tự (không nhảy giữa các node), các máy khác nhau thì rải đều.

### Chọn và giữ node

```
lần đầu: agent gọi lần lượt các node trong gói cài tới khi một node trả lời
   │
   ▼
node đó trả về danh sách node CỦA MÁY NÀY theo quy tắc gán server
   │
   ▼
agent dùng node đầu tiên gọi được trong danh sách đó
   │
   ▼
gửi heartbeat ── được ──▶ GIỮ node này (lưu lại, khởi động lại vẫn dùng node này)
   │
 lỗi (không kết nối được, hoặc node trả lỗi 5xx)
   │
   ▼
thử lần lượt các node khác theo thứ tự trong danh sách của máy ── được ──▶ giữ node mới, không tự quay về node cũ
   │
 hết danh sách
   │
   ▼
chờ rồi thử lại từ đầu (30 giây → 30 phút)
```

- Mỗi heartbeat, node trả về **số đời danh sách cụm** đang có. Agent thấy số mới hơn thì tải chuỗi quyết định `member.*` còn thiếu, kiểm chữ ký từng quyết định theo danh sách trước đó, rồi mới dùng.
- **Hỏi node khác định kỳ (Q8):** **mỗi 12 giờ** (lệch ngẫu nhiên ±20%, để hàng nghìn máy không hỏi cùng lúc) và mỗi lần agent khởi động, agent hỏi **một node khác bất kỳ, chọn ngẫu nhiên** trong danh sách cụm về số đời danh sách cụm. Thấy danh sách mới hơn thì tải, kiểm chữ ký rồi dùng. Nếu node đang giữ đã bị cô lập hay gỡ, agent **chuyển kênh ngay**.
- **Đánh đổi của 12 giờ:** một node bị hack rồi bị cô lập vẫn có thể giữ các agent đang bám nó **tối đa khoảng 14 giờ** (12 giờ + 20%), vì node bị hack sẽ không tự báo cho agent. Trong thời gian đó nó nhận được dữ liệu trạng thái của các máy này, nhưng **không ra được lệnh gì cho máy** (bản agent và bộ rule vẫn cần chữ ký quá nửa node). Muốn rút ngắn thì giảm chu kỳ này.
- Agent **không gửi dữ liệu** cho node bị cô lập, dù node đó vẫn trả lời.
- **Sự kiện chưa gửi được** (hàng đợi trên máy, mốc A3) được gửi cho node mới sau khi chuyển kênh.

### Đăng ký máy mới

Gửi đăng ký tới node đang giữ như hiện nay. Admin duyệt trên web bất kỳ node nào; duyệt thành quyết định `device.approve` (quá nửa). Có hiệu lực thì mọi node nhận máy.

## 10. Phát hành bản agent và bộ rule

### Bản agent (Q9)

```
admin tải bản agent lên node A (hoặc A tự build khi cập nhật server)
   │
   ▼
A tạo đề nghị release.publish: phiên bản, SHA-256 từng kiến trúc, kích thước, nhóm phát hành
   │
   ▼
mỗi node: tự tải file từ A, kiểm SHA-256
          → web node đó hiện "Bản agent chờ xác nhận"
          → admin ĐĂNG NHẬP CHÍNH NODE ĐÓ (mã của node đó) và bấm Xác nhận
          → node ký
   │
   ▼
đủ quá nửa chữ ký → bản agent có hiệu lực, mọi node phục vụ tải bản đó trên 6222
   │
   ▼
agent tải, kiểm SHA-256 và chữ ký của quá nửa node theo danh sách cụm → mới chạy
```

- **Không còn khóa phát hành chung.** Hack một node không ký được bản agent độc: cần thêm chữ ký của các node khác, và mỗi chữ ký cần admin xác nhận trên chính node đó.
- Quay về bản khung khi bản mới chết 3 lần (đã làm ở bản hiện tại) vẫn giữ nguyên.

### Bộ rule

`ruleset.publish`, **quá nửa** node ký (không cần admin xác nhận từng node, vì rule không chạy được lệnh trên máy). Agent kiểm chữ ký của quá nửa node thay vì khóa phát hành. Lệnh `scs-server rules check / test` giữ nguyên; `rules build` đổi thành gửi đề nghị vào cụm.

**Đã làm (2026-11):** kho bộ rule trên từng server (web trang "Rule quét log", menu mục 16). Server đã thuộc cụm thì bấm Duyệt gửi quyết định `ruleset.publish` với nội dung `{name, version, sha256}` (SHA-256 phần agent nhận, không mang cả bộ rule). Mọi node tự kiểm định dạng nội dung trước khi ký. Đủ quá nửa thì server mới ký bộ rule bằng khóa phát hành của nó; server khác có đúng file (cùng SHA) thì ký theo quyết định đã có, không bỏ phiếu lại. Cùng tên + phiên bản mà SHA khác bị chặn. **Chưa làm:** agent kiểm chữ ký của quá nửa node (vẫn kiểm khóa phát hành của server mình, mốc C4/C6), và chuyển file bộ rule giữa các node (admin thêm file trên từng server).

## 11. Mạng: LAN, Internet, NAT, DDNS, VPN

Mỗi node khai **danh sách địa chỉ cho từng cổng** (5222 cho node khác, 6222 cho agent), giống danh sách địa chỉ agent hiện nay. Bên gọi thử lần lượt. An toàn dựa vào khóa node nên đi đường nào cũng như nhau.

| Kiểu | Cách khai | Ghi chú |
|---|---|---|
| Cùng LAN / khác VLAN | `https://10.x.x.x:5222` | mở TCP 5222 giữa các node trên Cisco ACL |
| IP WAN tĩnh | `https://203.0.113.10:5222` | router chuyển tiếp cổng về 5222 |
| NAT, cổng khác | `https://203.0.113.10:15222` | cổng ngoài tùy chọn, về 5222 bên trong |
| DDNS | `https://ten.ddns.net:15222` | nên dùng khi IP WAN động |
| VPN (WireGuard, Tailscale) | `https://100.x.x.x:5222` | khỏi phải mở cổng ra Internet; khuyên dùng giữa các địa điểm |

- Đổi địa chỉ của một node là quyết định `member.address` (chữ ký của node đó + quá nửa). Agent và các node khác nhận qua heartbeat / nhật ký.
- Cổng 5222 mở ra Internet được: không có trang web, mọi kết nối phải có khóa node trong danh sách cụm, trừ yêu cầu xin vào cụm (giới hạn tần suất theo IP, tối đa 20 yêu cầu chờ). Áp dụng cùng giới hạn kết nối và kích thước như cổng 6222.
- **Đồng hồ:** mọi node bật NTP; lệch quá 5 phút thì từ chối, như agent.

## 12. Mất kết nối giữa các server (chia cụm)

- **Agent không bị ảnh hưởng**: vẫn gửi dữ liệu về node đang giữ; node vẫn nhận và lưu.
- **Phía còn quá nửa node** vẫn ra quyết định được.
- **Phía ít hơn quá nửa** chỉ đọc: vẫn nhận dữ liệu agent, web vẫn xem được (thiếu dữ liệu phía kia), nhưng **không duyệt, không đổi gì** cho tới khi nối lại. Đây là điều kiện để hai nửa không ra hai quyết định trái nhau.
- Nối lại: phía ít bắt kịp nhật ký từ phía nhiều; trạng thái chung tự hội tụ theo `last_seen`.
- Chia đôi bằng nhau (ví dụ 2 + 2) thì **không bên nào** ra được quyết định. Nên dùng số node lẻ.

## 13. Chuyển từ một server lên cụm

1. Server hiện tại chạy `cluster init`, thành node đầu tiên. Khóa node được sinh mới; dữ liệu và máy đã duyệt được ghi thành các quyết định ban đầu.
2. **Agent đã cài vẫn tin CA cũ.** Trong thời gian chuyển, chứng chỉ của node trên cổng 6222 là **chứng chỉ do CA cũ cấp cho chính khóa node**: agent cũ kiểm theo CA, agent mới kiểm khóa theo danh sách cụm. Cùng một chứng chỉ dùng được cho cả hai.
3. Node vẫn chấp nhận chứng chỉ máy do CA cũ cấp (agent cũ), song song với chứng chỉ tự ký từ khóa máy (agent mới).
4. Phát hành bản agent mới (hiểu danh sách cụm) theo cách cũ, lần cuối dùng khóa phát hành cũ. Khi mọi máy đã lên bản mới thì tắt đường CA và khóa phát hành cũ, rồi mới thêm node thứ hai.
5. Gói cài agent từ đó mang mã cụm và danh sách cụm thay cho CA.

## 14. Mối đe dọa và cách chặn

| Mối đe dọa | Cách chặn |
|---|---|
| Server lạ xin vào cụm | phải được **mọi** node duyệt; admin đối chiếu mã khóa trên từng node |
| Kẻ đứng giữa hai node | TLS hai chiều, chỉ nhận khóa có trong danh sách cụm |
| Kẻ đứng giữa agent và node | agent chỉ tin khóa node có trong danh sách cụm đã ký |
| Một node bị hack ra quyết định bậy | cần chữ ký quá nửa; node khác tự kiểm trước khi áp dụng |
| Một node bị hack **ký cả hai đề nghị trái nhau** để hai bản cùng đủ quá nửa (tách nhánh nhật ký) | **phát hiện, không chặn được với quy tắc quá nửa**: hai chữ ký trái nhau là bằng chứng chỉ đích danh node đó; node nào thấy hai bản cùng đủ chữ ký thì dừng ra quyết định và báo trên trạng thái cụm, chờ admin cô lập node gian và chọn nhánh đúng. Muốn **chặn hẳn** phải đổi sang quy tắc **hơn 2/3** (xem dưới) |
| Một node bị hack ký bản agent độc | cần quá nửa chữ ký, mỗi chữ ký cần admin xác nhận trên chính node đó |
| Một node bị hack giữ agent lại | agent mỗi 12 giờ hỏi một node khác; cô lập node đó là agent rời đi trong tối đa ~14 giờ |
| Một node bị hack khai sai trạng thái máy | chỉ khai sai được máy đang gửi về nó; cô lập thì bị bỏ |
| Một node bị hack từ chối ký (phá hoại) | quá nửa vẫn đủ; cô lập node đó tại chỗ ngay |
| Đánh cắp đường truyền rồi gửi lại gói cũ | quyết định có `seq`, `prev`, `nonce`; heartbeat có giờ + nonce (làm luôn phần kiểm nonce còn thiếu) |
| Hack quá nửa số node | **không chặn được**: đây là giới hạn của quy tắc quá nửa. Giảm rủi ro bằng cách đặt node ở nhiều nơi, người quản trị khác nhau |
| Lộ mã đăng nhập web của một node | chỉ ra được đề nghị từ node đó; việc nhạy cảm vẫn cần xác nhận trên các node khác |

### Quá nửa hay hơn 2/3

Quy tắc quá nửa giả định node có thể **tắt** nhưng không **gian**. Một node gian ký hai đề nghị trái nhau có thể làm cả hai cùng đủ quá nửa (cụm 3 node: A + gian, B + gian).

| | Quá nửa (đang dùng) | Hơn 2/3 |
|---|---|---|
| Số chữ ký cần, cụm 3 / 4 / 5 / 7 node | 2 / 3 / 3 / 4 | 3 / 3 / 4 / 5 |
| Chịu được số node tắt, cụm 3 / 4 / 5 / 7 | 1 / 1 / 2 / 3 | 0 / 1 / 1 / 2 |
| Một node gian ký hai lần | tách nhánh được, **bị phát hiện** và chỉ đích danh | **không tách nhánh được** |

**Đã chốt (2026-11-12): giữ quá nửa.** Với hơn 2/3, cụm 3 node tắt một là không ra được quyết định; phải từ 4 node mới chịu được một node tắt. Code tính số chữ ký ở một chỗ (`Majority` trong `common/cluster`), nên đổi quy tắc là sửa một hàm.

## 15. Thứ tự làm

| Mốc | Nội dung | Xong khi |
|---|---|---|
| **C1** (đã có) | Khóa node, chứng chỉ tự ký, cổng 5222 với TLS hai chiều + kiểm khóa, `cluster init` | 1 node chạy, gọi 5222 không có khóa đúng bị ngắt |
| **C2** | Nhật ký quyết định: đề nghị, ký, quá nửa, bắt kịp, chống ký trùng **(đã có)**; chuyển duyệt / thu hồi máy, cấu hình sang nhật ký (chưa) | 3 node trên một máy test: tắt 1 vẫn duyệt được máy, tắt 2 thì không; node bật lại tự bắt kịp |
| **C3** (đã có) | Thêm / cô lập / gỡ node, web "Cụm server" | thêm node cần đủ chữ ký mọi node; cô lập tại chỗ có hiệu lực ngay |
| **C4** | Agent theo cụm: kiểm danh sách cụm thay CA, quy tắc gán server, giữ node, chuyển kênh, hỏi node khác mỗi 12 giờ; bước chuyển cho agent cũ | agent cũ và mới cùng chạy; cô lập node là agent rời đi ở lần hỏi kế tiếp |
| **C5** | Trạng thái chung + web gom dữ liệu từ mọi node | web hiện đúng siêu thị online / offline khi máy chuyển kênh; node tắt thì web ghi rõ thiếu |
| **C6** | Phát hành bản agent và bộ rule bằng chữ ký quá nửa node | agent từ chối bản chỉ có chữ ký của một node |

Các mốc của bản một server (A3 đọc log, M4 lưu trạng thái máy, M5 gửi log, M7 phát rule) vẫn dùng lại được trong cụm: phần nhận dữ liệu của từng node không đổi.

## 16. Đã xác nhận (2026-11-12)

1. Thêm node cần **100%** node hợp lệ; mọi việc khác theo **quá nửa** (kể cả gỡ node).
2. Quá nửa chỉ tính **node hợp lệ** (không bị cô lập, không bị gỡ), đếm theo danh sách cụm, không theo số node đang trả lời ([mục 5](#còn-sống-và-hợp-lệ-tính-thế-nào)).
3. Bộ rule chỉ cần quá nửa chữ ký, không cần admin xác nhận từng node.
4. Lần đầu agent gọi một node, rồi nhận **danh sách node theo quy tắc gán server** ([mục 9](#quy-tắc-gán-server)).
5. Agent hỏi **một node khác bất kỳ, chọn ngẫu nhiên** mỗi **12 giờ** (và mỗi lần khởi động).
6. **Làm cụm ngay**, bắt đầu từ mốc C1.
7. **Giữ quy tắc quá nửa** (không đổi sang hơn 2/3): chấp nhận việc một node gian ký hai lần chỉ bị phát hiện chứ không bị chặn ([mục 14](#quá-nửa-hay-hơn-23)).

## 17. Dữ liệu siêu thị và tài khoản giữa nhiều server (mốc D1)

Trạng thái: **bản thiết kế (2026-11-19), chưa làm**. Phạm vi: **thông tin siêu thị** (bảng `stores`) và **tài khoản web**
(bảng `accounts`). Trạng thái realtime (máy online / offline, cảnh báo — mục 7) và dữ liệu lớn (mục 8) để mốc sau.

Có **hai hướng vận hành**, khác nhau ở cách giữ dữ liệu về sau:

| | Hướng A — chuyển nguyên server | Hướng B — server chủ + bản sao |
|---|---|---|
| Dùng khi | Chuyển **dữ liệu** sang một server khác **qua mạng** (ví dụ từ máy thử bên ngoài về server ở trụ sở), máy cũ không cần dời, chỉ thôi dùng | Chạy **nhiều server lâu dài**, một server (thường ở trụ sở) giữ dữ liệu gốc |
| Dữ liệu mang theo | **Toàn bộ**: máy, chứng chỉ, CA, khóa phát hành, siêu thị, tài khoản, lịch sử | Siêu thị + tài khoản (D1). Máy / chứng chỉ cần thêm C2 + C4 |
| Cần cụm | Không | Có (mục 5–6) |
| Code mới | Không (đã có: sao lưu DB, danh sách địa chỉ cho agent) | Có — mục 17.2 trở đi |

**Nguyên tắc chung cho cả hai: bảo vệ dữ liệu đặt trên tốc độ và tiện lợi.** Hai bảng có dữ liệu cá nhân (email, tên
người phụ trách) và dữ liệu ảnh hưởng bảo mật (mức quyền; IP WAN siêu thị — điều kiện duyệt tự động máy).

### 17.1 Hướng A — chép nguyên dữ liệu server sang server khác qua mạng (đã làm được)

Không dời máy: máy cũ để nguyên chỗ, dữ liệu được chép qua SSH sang server mới. 
Giữ **đúng CA và khóa phát hành** thì agent đang chạy vẫn tin server mới, không cài lại máy nào.

1. **Chuẩn bị máy mới** (trụ sở): menu mục 1 → 2 (Docker, Go, PostgreSQL, test). **Chưa** chạy mục 3.
2. **Cho agent biết địa chỉ mới trước** (máy cũ, menu mục 15): thêm IP / tên miền của máy mới, mở chuyển tiếp cổng 6222
   về máy mới. Chờ trang "Địa chỉ server" báo **gần 100% máy đã nhận danh sách** (≤ 65 phút).
3. **Dừng ghi ở máy cũ**: `sudo systemctl stop scs-server` (agent giữ sự kiện trong hàng đợi đĩa, không mất).
4. **Sao lưu ở máy cũ** (menu mục 18 → 1) và gom các file bí mật:
   `/etc/scs-server/` (server.env, pki/ca.key, pki/release.key, chứng chỉ), `/var/lib/scs-server/data/cluster/` (nếu có),
   `agent-servers.json`, kho rule.
5. **Chép sang máy mới chỉ qua kênh mã hóa** (`scp` / `rsync -e ssh`, khóa SSH, không mật khẩu); **không** USB, mail,
   chat, ổ chia sẻ. Kiểm `sha256sum` hai đầu.
6. **Máy mới**: khôi phục DB (menu mục 18 → 3), đặt file vào đúng chỗ với **đúng quyền** (khóa 600, user `scs`), chạy
   menu mục 3 (giữ CA cũ; thêm IP mới vào chứng chỉ server), kiểm `/readyz`, đăng nhập web, xem máy gọi về.
7. **Sau 1–2 ngày ổn định**: gỡ địa chỉ cũ (menu mục 15, khi "máy đang dùng" về 0), rồi **xóa sạch** máy cũ: DB, bản sao
   lưu, `/etc/scs-server`, khóa (`shred` hoặc xóa ổ), thu hồi khóa SSH / mật khẩu ứng dụng Gmail nếu chuyển sang tài khoản
   khác.

Rủi ro chính là lúc chép: trong một khoảng ngắn có **hai bản** CA, khóa phát hành và email người dùng. Giảm: chép một
lần, qua SSH, xóa máy cũ ngay khi xong; không giữ bản trung gian ở máy cá nhân. Bước 3–6 đã có
script: menu mục 20 / `scripts/move-server.sh export | import` (scp + SHA-256 hai đầu, xóa bản tạm bằng `shred`) — xem
INSTALL.md.

### 17.2 Hướng B — mô hình server chủ

- Trong cụm có **đúng một server chủ dữ liệu** (gọi tắt **chủ**). Chủ giữ bản gốc của siêu thị và tài khoản; **mọi thay
  đổi chỉ làm trên chủ**. Các server khác giữ **bản sao chỉ đọc**, kéo thay đổi từ chủ.
- **Chọn chủ bằng biểu quyết**: quyết định mới **`data.primary`** `{node: "<mã node>"}` trong nhật ký cụm, cần **quá nửa
  node hợp lệ**, **mỗi chữ ký phải được admin xác nhận trên chính server đó** (như bỏ cô lập node). Đổi chủ = biểu quyết
  lại; mỗi lần đổi tăng **nhiệm kỳ** (`term`).
- Chủ bị cô lập / gỡ → cụm **không có chủ**: mọi server chỉ đọc hai bảng này (vẫn đăng nhập, vẫn xem) tới khi biểu quyết
  chủ mới.
- Server bản sao: web hiện "Dữ liệu siêu thị / tài khoản do **<chủ>** quản lý — sửa tại server đó" và nút mở web của chủ;
  form sửa, nạp file, duyệt tài khoản bị tắt (server cũng chặn API, 409).
- **Đăng ký tài khoản** chỉ trên chủ (trang đăng ký ở bản sao chỉ dẫn sang chủ). Tài khoản chờ duyệt chỉ nằm ở chủ.
- Đăng nhập: **mọi server** — mỗi server tự gửi mã qua Gmail của nó, phiên riêng từng server như hiện nay.

### 17.3 Nhật ký thay đổi của chủ

Mỗi thay đổi trên chủ (sửa một siêu thị, một lần nạp file, duyệt / đổi mức / khóa / xóa tài khoản, giới hạn phiên) thành
**một mục thay đổi** trong bảng `data_changes` của chủ:

```json
{ "term": 3, "seq": 1052, "prev": "<sha256 mục 1051>", "at": "...", "kind": "stores | accounts",
  "actor": "<người thao tác>", "rows": [ { "key": "13645", "rev": 7, "data": { ... } } ],
  "sig": "<chữ ký khóa node của chủ>" }
```

- **Chuỗi băm** (`prev`) + `seq` tăng liên tục + chữ ký chủ: bản sao phát hiện mất mục, đảo thứ tự, mục giả hay bị sửa.
- `term` phải khớp nhiệm kỳ của quyết định `data.primary` mới nhất: chủ cũ (đã bị thay) **không còn ghi được** vào bản sao.
- Mỗi dòng có `rev` (số đời): chủ là nơi duy nhất ghi nên không có xung đột; `rev` dùng để bản sao kiểm và để chặn hai
  người sửa cùng lúc trên web chủ.
- Giữ mục thay đổi 30 ngày; bản sao chậm hơn thế thì lấy **bản chụp** (17.4).

### 17.4 Kênh giao tiếp giữa các server

Dùng lại cổng **5222** (TLS 1.3 hai chiều, chỉ nhận khóa node trong danh sách cụm; node bị cô lập bị từ chối ngay). Thêm
đường dẫn **chỉ chủ trả lời, chỉ node hợp lệ gọi được**:

| Đường dẫn | Việc | Giới hạn |
|---|---|---|
| `GET /cluster/v1/data/changes?after=<seq>` | các mục thay đổi sau `seq` | tối đa 2 MB / lần |
| `GET /cluster/v1/data/snapshot/{stores\|accounts}?after=&limit=` | bản đầy đủ theo trang, kèm `term` + `seq` ứng với bản chụp và chữ ký chủ trên dấu SHA-256 cả bảng | 1.000 dòng / trang; tối đa 1 bản chụp đầy đủ / node / 10 phút |
| `GET /cluster/v1/data/digest` | `term`, `seq` mới nhất, SHA-256 từng bảng | nhẹ |

- Bản sao **kéo** (pull) mỗi **60 giây** (`digest` → có `seq` mới thì `changes`); chủ không đẩy. Chủ mới có thay đổi thì
  báo nhẹ qua `status` để bản sao kéo sớm.
- Bản sao **kiểm trước khi áp dụng**: chữ ký chủ, `term` đúng quyết định `data.primary`, chuỗi `prev` liền, `seq` liên
  tục, và **kiểm lại toàn bộ nội dung bằng bộ kiểm của chính nó** (định dạng, độ dài, IP hợp lệ, mức 1–7, ký tự vô hình).
  Sai một chỗ là **bỏ cả lô, dừng kéo, báo trên trang Cụm server** — không áp dụng nửa vời.
- Áp dụng trong **một transaction**, ghi `audit` tại bản sao: `term/seq`, chủ, người thao tác gốc, số dòng.
- Không có đường nào để node khác **đọc tùy ý** một tài khoản hay siêu thị: chỉ kéo được theo chuỗi thay đổi / bản chụp.

### 17.5 Giới hạn băng thông: 10 Mbit/s

- Một bộ giới hạn chung cho phần thân dữ liệu đồng bộ: mỗi server **gửi ≤ 10 Mbit/s** và **nhận ≤ 10 Mbit/s** (token
  bucket), cấu hình `SCS_CLUSTER_SYNC_MBPS` (mặc định 10). Bỏ phiếu, chữ ký, `status` không tính (để cụm không kẹt).
- Chủ phục vụ **tối đa 2 lượt chuyển cùng lúc**; quá thì 503 + `Retry-After`.
- Ước lượng: 20.000 siêu thị đủ thông tin ≈ 6–8 MB → ≈ 6 giây; 1.000 tài khoản ≈ 0,3 MB; một lần nạp file vài trăm KB.

### 17.6 Bảo vệ dữ liệu

| Mục tiêu | Cách làm |
|---|---|
| Server bản sao không sửa được dữ liệu | Không có API ghi ở bản sao (409); chỉ áp dụng mục có chữ ký chủ đúng nhiệm kỳ |
| Chọn / đổi chủ phải có người | `data.primary` cần quá nửa node, **admin xác nhận trên từng server** |
| Phát hiện giả, mất, sửa | Chuỗi băm + `seq` + chữ ký; mỗi giờ so `digest`, lệch → cảnh báo, tự lấy bản chụp |
| **Chủ bị hack cấp quyền bậy** | Bản sao áp dụng ngay nhưng **ghi nổi bật** mọi lần cấp / nâng **mức 1–4** trên trang Tài khoản của từng server; admin bản sao **khóa tại chỗ** được một tài khoản (chỉ trên server đó, không cần chủ). Nghi chủ bị hack: cô lập chủ (quá nửa) → hết quyền ghi ngay, biểu quyết chủ mới |
| Dữ liệu cá nhân không rải rác | Nhật ký cụm (`log.jsonl`) chỉ có `data.primary`, không có email / tên; mục thay đổi chỉ trong DB, không ghi file tạm |
| Chỉ đồng bộ cái cần | Không đồng bộ: phiên, mã đăng nhập, `login_blocks`, `last_login_at`, `audit`, tài khoản chờ duyệt; mức 0 không bao giờ đồng bộ |
| Truy vết | `audit` ở chủ (người sửa) + ở bản sao (đã áp dụng gì, từ chủ nào, nhiệm kỳ nào) |

**Đánh đổi so với bản đề xuất trước (mỗi thay đổi cần quá nửa ký):** mô hình chủ đơn giản, nhanh, dễ vận hành hơn,
nhưng **tin chủ nhiều hơn** — chủ bị hack thì đổi được dữ liệu cho cả cụm tới khi bị cô lập. Bù lại bằng: chọn chủ phải
biểu quyết, nâng quyền 1–4 hiện nổi bật ở mọi server, khóa tại chỗ, cô lập chủ có hiệu lực ngay.

### 17.7 Vào cụm và chuyển chủ

1. **Cụm 1 server** (server đang chạy): `cluster init` → tự là chủ (`data.primary` nhiệm kỳ 1 trong quyết định đầu).
2. **Thêm server** (100% đồng ý như hiện nay). Server mới **đã có dữ liệu riêng**: **sao lưu DB rồi thay bằng bản chụp
   từ chủ**; phần chỉ có ở server mới được liệt kê ra file để admin nạp lại trên chủ (không tự trộn).
3. **Chuyển chủ về trụ sở**: thêm server trụ sở → chờ bản sao bắt kịp (`digest` khớp) → biểu quyết `data.primary` =
   trụ sở → server cũ thành bản sao (hoặc gỡ khỏi cụm, rồi xóa sạch như 17.1 bước 7).

### 17.8 Rủi ro còn lại

| Rủi ro | Mức | Ghi chú |
|---|---|---|
| Chủ bị hack đổi dữ liệu cả cụm | Trung bình | Phát hiện qua nâng quyền nổi bật / audit; cô lập chủ; hack quá nửa thì không chặn được |
| Dữ liệu cá nhân nằm trên nhiều máy | Trung bình | Bản chất của bản sao. Mã hóa ổ đĩa, mã hóa sao lưu (R17) |
| Server bị gỡ vẫn giữ bản dữ liệu đã chép | Trung bình | Xóa DB khi gỡ; chỉ thêm server do người tin cậy giữ |
| Chủ tắt → không sửa được siêu thị / tài khoản | Thấp | Đăng nhập, xem vẫn được; biểu quyết chủ khác nếu tắt lâu |
| Bản sao chậm tới 60 giây | Thấp | Khóa / xóa tài khoản trên chủ tới bản sao trong ≤ 1 phút; cần ngay thì khóa tại chỗ |

### 17.9 Thứ tự làm D1

| Bước | Nội dung |
|---|---|
| D1.1 | Cột `rev` cho `stores`, `accounts`; bảng `data_changes`; ghi mục thay đổi trong cùng transaction với mọi lần sửa trên chủ |
| D1.2 | Quyết định `data.primary` (quá nửa + admin xác nhận từng server), nhiệm kỳ; web Cụm server: biểu quyết chủ |
| D1.3 | Đường dẫn `changes` / `snapshot` / `digest` trên 5222, chữ ký chủ, giới hạn 10 Mbit/s + 2 lượt |
| D1.4 | Bản sao: kéo 60 giây, kiểm, áp dụng, dừng khi sai; chặn API ghi ở bản sao (409); web hiện "do <chủ> quản lý" |
| D1.5 | Nâng quyền 1–4 nổi bật + khóa tại chỗ; server mới có dữ liệu riêng: sao lưu + thay; test cụm 3 server trên PostgreSQL thật |

Đã chốt: giới hạn **10 Mbit/s** (2026-11-19); mô hình **server chủ do admin các server biểu quyết** thay cho "mỗi thay đổi
cần quá nửa ký" (2026-11-19).

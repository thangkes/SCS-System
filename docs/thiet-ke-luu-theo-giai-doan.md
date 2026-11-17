# Thiết kế: lưu sự kiện và trạng thái máy theo giai đoạn

> Trạng thái (2026-11-17): **đã làm phía server** (bước 1–4 mục 13 và cổng nhận sự kiện `POST /v1/logs`).
> Chưa làm: agent đọc log liên tục và gửi sự kiện (mốc A3). Thay cách lưu "mỗi sự kiện / mỗi heartbeat
> một dòng" trong kế hoạch cũ (bảng `events`, `heartbeats`, `device_days` của migration 0001, chưa từng có
> dữ liệu, migration 0004 đã bỏ).

## 1. Ý tưởng

Không lưu từng sự kiện, từng nhịp heartbeat. Mỗi máy có một **dòng thời gian gồm các giai đoạn**. Mỗi giai đoạn
là **một dòng** trong DB, có giờ bắt đầu, giờ cuối cùng nghe tin, và bản tóm tắt những gì xảy ra trong đó.

```
máy 12345-TN1-PRI
10:00  medium SVC-FAILED-01           → mở  OK #1 (từ 10:00)
11:00  medium SVC-FAILED-01           →     OK #1 kéo tới 11:00 (1 giờ), SVC-FAILED-01 × 2
12:00  (chỉ heartbeat)                →     OK #1 kéo tới 12:00 (2 giờ)
12:20  high   SSH-ROOT-LOGIN-01       → đóng OK #1 lúc 12:20, mở CẢNH BÁO #2 (mức high)
12:40  medium SVC-FAILED-01           →     tính vào CẢNH BÁO #2
13:05  critical RAID-01               →     CẢNH BÁO #2 lên mức critical (chỉ admin đóng được)
14:00  admin bấm "Đã xử lý"           → đóng CẢNH BÁO #2, mở OK #3 (từ 14:00)
```

Cả buổi chỉ có 3 dòng thay vì một dòng cho mỗi sự kiện và mỗi 5 phút heartbeat.

## 2. Quyết định đã chốt

| # | Câu hỏi | Chốt |
|---|---|---|
| 1 | OK kéo dài theo log hay theo heartbeat | **Theo cả heartbeat**: máy còn gửi heartbeat là giai đoạn đang mở kéo dài, không cần có log |
| 2 | Đóng cảnh báo | Mức **high**: **tự đóng** khi hết hạn mà không có sự kiện high / critical mới. Mức **critical**: **chỉ admin** xác nhận mới đóng |
| 3 | Mất liên lạc | **Hiện cảnh báo**, **tự đóng** khi máy gửi lại (heartbeat hoặc log) |
| 4 | Heartbeat | Cũng không lưu từng nhịp: chỉ ghi khi trạng thái máy **thay đổi đáng kể** |

## 3. Ba loại giai đoạn

| Loại | Mở khi | Kéo dài khi | Đóng khi |
|---|---|---|---|
| **OK** (`ok`) | Máy được duyệt; cảnh báo / mất liên lạc vừa đóng | Có heartbeat hoặc sự kiện `low` / `medium` | Có sự kiện `high` / `critical`; mất liên lạc |
| **Cảnh báo** (`alert`) | Sự kiện `high` / `critical` | Mọi heartbeat và sự kiện (kể cả `low` / `medium`) đều tính vào | high: tự đóng sau hạn (mục 5); critical: admin bấm "Đã xử lý" |
| **Mất liên lạc** (`offline`) | Quá hạn không nghe máy gửi gì khi đang OK | — | Máy gửi lại bất kỳ gì |

Mỗi máy luôn có **đúng một** giai đoạn đang mở. Giai đoạn đóng thì giai đoạn sau mở **ngay tại giờ đó**,
nên dòng thời gian không có lỗ.

**Mất liên lạc khi đang có cảnh báo:** cảnh báo **giữ nguyên, không đóng** (mất liên lạc không được che sự cố
đang mở). Giai đoạn cảnh báo ghi thêm "mất liên lạc từ …"; máy gửi lại thì xóa ghi chú đó, cảnh báo tiếp tục
chờ đóng theo luật của nó.

## 4. Mức nghiêm trọng

Dùng đúng các mức của rule (`severity` trong file rule):

- `info`, `low`, `medium`: không đổi giai đoạn, chỉ cộng vào bản tóm tắt.
- `high`: mở cảnh báo (hoặc cộng vào cảnh báo đang mở), **tự đóng được**.
- `critical`: mở cảnh báo hoặc **nâng** cảnh báo đang mở lên critical. Đã lên critical thì **chỉ admin đóng**,
  kể cả khi sau đó chỉ còn sự kiện high.

## 5. Hạn tự đóng và hạn mất liên lạc (đề xuất, cấu hình được)

| Biến | Mặc định đề xuất | Nghĩa |
|---|---|---|
| `SCS_ALERT_AUTOCLOSE` | **24h** (chốt) | Cảnh báo mức high tự đóng khi đã 24 giờ không có sự kiện high / critical mới. Cho phép 1h – 720h |
| `SCS_OFFLINE_AFTER` | **15m** (chốt) | 3 nhịp heartbeat (5 phút) không nghe gì → mất liên lạc. Cho phép 10m – 24h |

Server kiểm mỗi phút (tiến trình `worker` hoặc `all`).

Tự đóng ghi rõ "tự đóng" và giờ, admin vẫn xem lại được. Cảnh báo tự đóng mà sự cố quay lại thì mở cảnh báo mới.

## 6. Mỗi giai đoạn lưu gì

Bảng `device_periods` (migration mới, thay `events` / `heartbeats` / `device_days`):

| Cột | Nghĩa |
|---|---|
| `id`, `device_id` | |
| `kind` | `ok` / `alert` / `offline` |
| `started_at` | giờ bắt đầu |
| `ended_at` | giờ đóng (NULL = đang mở) |
| `severity` | mức cao nhất trong giai đoạn |
| `events` | tổng số sự kiện |
| `rules` | tóm tắt theo rule (jsonb): `{rule_id: {severity, count, first, last, sample}}`, `sample` là **một dòng log mẫu** (đã cắt 2048 ký tự). Tối đa 64 rule / giai đoạn, quá thì gom vào `"_khac"` |
| `offline_since` | (cảnh báo) đang mất liên lạc từ lúc nào |
| `closed_by` | `admin:<tên>` / `auto` / `system` (sự kiện mới, máy gửi lại) |
| `note` | ghi chú khi admin xử lý (≤ 500 ký tự) |

Ràng buộc trong DB: mỗi máy chỉ một giai đoạn mở (`UNIQUE (device_id) WHERE ended_at IS NULL`); `ended_at ≥
started_at`; `kind`, `severity` trong danh sách; `rules` ≤ 16 KB.

Cảnh báo vẫn hiện ở trang **"Cảnh báo"** như hiện nay: mỗi giai đoạn `alert` / `offline` gắn với một dòng trong
bảng `alerts` (loại `event` / `offline`). Nút xác nhận trên web = "Đã xử lý" = đóng giai đoạn.

## 7. Trạng thái máy (heartbeat)

- `device_status`: **một dòng mỗi máy**, ghi đè mỗi heartbeat (đã có trong 0001). Đây là ghi duy nhất mỗi nhịp.
  Giờ của heartbeat này cũng là giờ cuối cùng của giai đoạn đang mở, nên không phải cập nhật giai đoạn.
- `device_status_changes`: **chỉ ghi khi trạng thái thay đổi đáng kể** so với lần trước, gồm:
  hệ điều hành, kernel, bản agent, bộ rule đang dùng, IP; dịch vụ đổi trạng thái (active → failed…);
  RAID (degraded, đang đồng bộ); đồng bộ giờ; ổ đĩa **vượt mốc 80 / 90 / 95%** (lên hoặc xuống).
  Không ghi khi chỉ RAM, tải CPU, uptime, dung lượng trống thay đổi nhỏ.
- Server so bằng "dấu" của các trường trên (giữ trong RAM, lần đầu đọc từ DB), nên phần lớn heartbeat chỉ là
  một lệnh `UPDATE`.

## 8. Sự kiện từ agent (làm cùng mốc A3)

- `low` / `medium`: agent **gom theo rule** và gửi kèm heartbeat (số lần, lần đầu, lần cuối, một dòng mẫu), không
  gửi từng sự kiện. Server cộng vào giai đoạn đang mở: **một lệnh `UPDATE` mỗi heartbeat có sự kiện**.
- `high` / `critical`: agent gửi **ngay** (không chờ heartbeat), server đổi giai đoạn trong một transaction.
- Agent vẫn gom trước bằng `aggregate` và `cooldown_s` của rule như hiện nay.
- Mất mạng: agent giữ phần đã gom trên đĩa (giới hạn dung lượng), gửi khi có mạng lại; server dùng `agent_time`
  để đặt sự kiện vào đúng giai đoạn nếu còn hợp lý, quá cũ thì tính vào giai đoạn hiện tại kèm ghi chú.

## 9. Lượng ghi vào DB

Ví dụ 5.000 máy, heartbeat 5 phút: khoảng **17 lệnh UPDATE / giây** cho `device_status`, cộng vài dòng
`device_status_changes` và `device_periods` mỗi phút. Cách cũ là thêm khoảng 17 dòng mới / giây vào
`heartbeats` cộng mỗi sự kiện một dòng, rồi phải nén và xóa theo ngày.

## 10. Giữ dữ liệu

- `device_periods`, `device_status_changes`: xóa khi **đã đóng hơn 60 ngày** (quy tắc 60 ngày của dự án).
  Giai đoạn còn mở giữ nguyên dù đã kéo dài bao lâu.
- `alerts`: như hiện nay (60 ngày).
- Bỏ các bảng `events`, `heartbeats`, `device_days` và chính sách nén / xóa của Timescale cho hai bảng đó
  (chưa có dữ liệu). TimescaleDB vẫn giữ trong cài đặt cho việc khác sau này.

## 11. Cụm nhiều server

Giai đoạn lưu ở **server máy đang gửi về** (đúng nguyên tắc "dữ liệu agent ở server nhận"). Agent giữ một server
tới khi lỗi; chuyển server thì server mới mở giai đoạn của nó, web gom dòng thời gian từ mọi server (mốc C5).
"Mất liên lạc" chỉ báo khi **mọi** server đều không nghe máy (sau C5); trước C5 mỗi server tự báo.

## 12. Web

- Trang máy: **dòng thời gian** các giai đoạn (xanh OK, vàng / đỏ cảnh báo, xám mất liên lạc), bấm vào xem tóm
  tắt theo rule và dòng log mẫu.
- Trang "Cảnh báo": như hiện nay, thêm nút "Đã xử lý" (ghi chú tùy chọn); cảnh báo critical ghi rõ "cần admin
  xác nhận".
- Trang Tổng quan: số máy đang cảnh báo / mất liên lạc.

## 13. Thứ tự làm (bước 1–4 đã làm)

1. Migration mới: `device_periods`, `device_status_changes`, bỏ `events` / `heartbeats` / `device_days`.
2. Bộ máy chuyển giai đoạn (thuần Go, test đủ các trường hợp mục 3–5).
3. Heartbeat ghi `device_status` + `device_status_changes`; kiểm mất liên lạc định kỳ; tự đóng cảnh báo high.
4. API + web: dòng thời gian, "Đã xử lý".
5. Cùng mốc A3: agent gom sự kiện, gửi kèm heartbeat / gửi ngay khi high; server cộng vào giai đoạn.

Bước 1–4 làm được ngay (chỉ cần heartbeat); bước 5 cần agent đọc log liên tục.

-- Trạng thái xử lý sự cố (giai đoạn cảnh báo đang mở) và lý do admin đóng.
--   handling: waiting_info (Chờ thông tin) | reported (Đã gửi report) — sự cố VẪN MỞ; tự đóng theo mức như cũ
--             (high tự đóng khi yên đủ lâu, critical cần người đóng). handling_log: lịch sử đổi trạng thái (≤ 50 mục).
--   resolution: fixed (Đã xử lý) | confirmed (Xác nhận sự cố — đóng nhanh sự cố một lần); chỉ khi admin đóng.
ALTER TABLE device_periods
    ADD COLUMN handling      text CHECK (handling IN ('waiting_info', 'reported')),
    ADD COLUMN handling_by   text CHECK (length(handling_by) <= 90),
    ADD COLUMN handling_at   timestamptz,
    ADD COLUMN handling_note text CHECK (length(handling_note) <= 500),
    ADD COLUMN handling_log  jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(handling_log) = 'array'),
    ADD COLUMN resolution    text CHECK (resolution IN ('fixed', 'confirmed'));

-- Sự cố admin đã đóng trước đây: tính là "Đã xử lý".
UPDATE device_periods SET resolution = 'fixed' WHERE closed_by LIKE 'admin:%';

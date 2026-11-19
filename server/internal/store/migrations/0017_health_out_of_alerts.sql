-- Sức khỏe máy tách khỏi cảnh báo: bản 6963dd3 ghi vấn đề sức khỏe thành sự kiện "may.<mã>" và mở giai đoạn cảnh báo.
-- Đóng các cảnh báo đang mở CHỈ gồm sự kiện đó (không có sự kiện log nào, không đang mất liên lạc), cùng dòng tương ứng
-- trong bảng alerts (trang Cảnh báo) — giống khi hệ thống tự đóng một giai đoạn. Heartbeat kế tiếp của máy mở lại giai
-- đoạn OK. Cảnh báo có lẫn sự kiện log giữ nguyên (admin xử lý như thường).
WITH closed AS (
    UPDATE device_periods
    SET ended_at = greatest(now(), started_at), closed_by = 'system',
        note = 'Sức khỏe máy không còn tạo cảnh báo (xem thẻ Tình trạng máy)'
    WHERE ended_at IS NULL AND kind = 'alert' AND offline_since IS NULL
      AND rules <> '{}'::jsonb
      AND NOT EXISTS (SELECT 1 FROM jsonb_object_keys(rules) AS k WHERE k NOT LIKE 'may.%')
    RETURNING id, ended_at
)
UPDATE alerts a SET acked_at = c.ended_at, acked_by = 'hệ thống'
FROM closed c
WHERE a.period_id = c.id AND a.acked_at IS NULL;

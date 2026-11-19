-- Sức khỏe máy (mốc A2): server tính từ trạng thái agent gửi trong heartbeat — mức ok / warn / bad và các vấn đề
-- đang có (ổ đầy, chỉ đọc, lỗi I/O, RAID hỏng, dịch vụ dừng, đồng hồ lệch, RAM thấp, nhiệt độ). Ghi đè mỗi heartbeat;
-- vấn đề mới xuất hiện được ghi thành sự kiện "may.<mã>" trong giai đoạn của máy.
ALTER TABLE device_status ADD COLUMN health jsonb CHECK (health IS NULL OR octet_length(health::text) <= 16000);

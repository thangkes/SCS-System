-- Thông báo tức thời cho trang Thiết bị LAN / Sức khỏe máy: thời điểm dữ liệu THỰC SỰ đổi (không phải lần ghi lại
-- định kỳ), để luồng thông báo chỉ báo trình duyệt khi có thay đổi.
--   lan_sites.changed_at:     thiết bị đổi trạng thái, siêu thị mới, máy báo khác lần trước (bitmap, IP, lệch).
--   device_status.health_at:  ghi nhận sức khỏe (health) khác lần heartbeat trước.
ALTER TABLE lan_sites ADD COLUMN changed_at timestamptz;
ALTER TABLE device_status ADD COLUMN health_at timestamptz;
CREATE INDEX device_status_health_at ON device_status (health_at);
CREATE INDEX lan_sites_changed_at ON lan_sites (changed_at);

-- Duyệt và thay thế máy cũ (cài lại máy): máy mới ghi "thay thế cho" máy cũ (đã thu hồi) để xem lại lịch sử.
ALTER TABLE devices ADD COLUMN replaces_device bigint REFERENCES devices(id);
CREATE INDEX devices_replaces ON devices (replaces_device) WHERE replaces_device IS NOT NULL;

-- Hạn chứng chỉ máy (90 ngày) để web theo dõi gia hạn. Máy đã có chứng chỉ trước bản này: tính từ lần cấp
-- gần nhất trong nhật ký thao tác (device.cert_issued) + 90 ngày.
ALTER TABLE devices ADD COLUMN cert_expires timestamptz;

UPDATE devices d SET cert_expires = a.last_at + interval '90 days'
FROM (SELECT device_id, max(at) AS last_at FROM audit WHERE action = 'device.cert_issued' GROUP BY device_id) a
WHERE a.device_id = d.id AND d.cert_serial IS NOT NULL;

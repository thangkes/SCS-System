-- Bộ rule agent đang cài (agent báo trong heartbeat): web biết máy nào đã áp dụng bộ nào.
-- {installed, name, version, sha256, total, active, scanning, error}; NULL = agent bản cũ, không báo.
ALTER TABLE device_status ADD COLUMN rules jsonb
    CHECK (rules IS NULL OR (jsonb_typeof(rules) = 'object' AND pg_column_size(rules) <= 2048));
CREATE INDEX device_status_rules ON device_status ((rules->>'name'), (rules->>'version')) WHERE rules IS NOT NULL;

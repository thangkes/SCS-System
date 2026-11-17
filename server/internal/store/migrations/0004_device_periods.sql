-- Lưu theo giai đoạn (docs/thiet-ke-luu-theo-giai-doan.md): không lưu từng sự kiện, từng nhịp heartbeat.
-- Mỗi máy có một dòng thời gian gồm các giai đoạn ok / alert / offline; mỗi giai đoạn là một dòng.
-- Trạng thái máy: một dòng mỗi máy (device_status, ghi đè), lịch sử chỉ ghi khi thay đổi đáng kể.

-- Bỏ các bảng ghi từng dòng của 0001 (chưa từng có dữ liệu). Xóa hypertable thì Timescale tự bỏ
-- luôn chính sách nén / xóa theo ngày của bảng đó.
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS heartbeats;
DROP TABLE IF EXISTS device_days;

CREATE TABLE device_periods (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id     bigint NOT NULL REFERENCES devices(id),
    kind          text NOT NULL CHECK (kind IN ('ok', 'alert', 'offline')),
    started_at    timestamptz NOT NULL,
    ended_at      timestamptz,                         -- NULL = đang mở
    severity      text CHECK (severity IS NULL OR severity IN ('info', 'low', 'medium', 'high', 'critical')),
    events        integer NOT NULL DEFAULT 0 CHECK (events >= 0),
    -- Tóm tắt theo rule: {rule_id: {severity, count, first, last, sample}}, tối đa 64 rule.
    rules         jsonb NOT NULL DEFAULT '{}'
                  CHECK (jsonb_typeof(rules) = 'object' AND pg_column_size(rules) <= 262144),
    offline_since timestamptz,                         -- alert: đang mất liên lạc từ
    last_serious  timestamptz,                         -- alert: sự kiện high / critical gần nhất
    closed_by     text CHECK (length(closed_by) <= 90), -- auto | system | admin:<tên>
    note          text CHECK (length(note) <= 500),
    CHECK (ended_at IS NULL OR ended_at >= started_at),
    CHECK ((ended_at IS NULL) = (closed_by IS NULL)),
    CHECK (kind = 'alert' OR (offline_since IS NULL AND last_serious IS NULL))
);
-- Mỗi máy chỉ một giai đoạn đang mở.
CREATE UNIQUE INDEX device_periods_open ON device_periods (device_id) WHERE ended_at IS NULL;
CREATE INDEX device_periods_device ON device_periods (device_id, started_at DESC);
CREATE INDEX device_periods_ended ON device_periods (ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX device_periods_open_kind ON device_periods (kind) WHERE ended_at IS NULL;

-- Lịch sử trạng thái máy: chỉ khi "dấu" trạng thái đổi (OS, kernel, bản agent, bộ rule, IP, dịch vụ,
-- RAID, đồng bộ giờ, ổ đĩa vượt mốc 80/90/95%). Không ghi khi chỉ RAM, tải CPU, uptime thay đổi.
CREATE TABLE device_status_changes (
    id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id bigint NOT NULL REFERENCES devices(id),
    at        timestamptz NOT NULL DEFAULT now(),
    changes   jsonb NOT NULL CHECK (jsonb_typeof(changes) = 'object' AND pg_column_size(changes) <= 16384)
);
CREATE INDEX device_status_changes_device ON device_status_changes (device_id, at DESC);
CREATE INDEX device_status_changes_at ON device_status_changes (at);

-- Dấu trạng thái lần trước (SHA-256 hex) để biết heartbeat này có thay đổi đáng kể không.
ALTER TABLE device_status ADD COLUMN sig text CHECK (sig IS NULL OR sig ~ '^[0-9a-f]{64}$');

-- Cảnh báo của một giai đoạn alert (kind 'event'): đóng giai đoạn = đóng cảnh báo và ngược lại.
ALTER TABLE alerts ADD COLUMN period_id bigint REFERENCES device_periods(id) ON DELETE SET NULL;
CREATE INDEX alerts_period ON alerts (period_id) WHERE period_id IS NOT NULL;

-- Dọn dẹp: giai đoạn và lịch sử trạng thái đã đóng hơn 60 ngày (giai đoạn còn mở giữ nguyên).
CREATE OR REPLACE PROCEDURE scs_cleanup(job_id integer, config jsonb) LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM seen_nonces WHERE received_at < now() - INTERVAL '1 day';
    DELETE FROM alerts WHERE created_at < now() - INTERVAL '60 days';
    DELETE FROM device_status_changes WHERE at < now() - INTERVAL '60 days';
    DELETE FROM device_periods WHERE ended_at < now() - INTERVAL '60 days';
END
$$;

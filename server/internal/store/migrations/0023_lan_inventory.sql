-- Kiểm kê thiết bị LAN (trang "Thiết bị LAN", chỉ tham khảo). Xem server/internal/lan.
--   lan_reporters: báo cáo gần nhất của từng máy (1 dòng / máy, ghi đè; chỉ ghi khi đổi hoặc mỗi giờ).
--   lan_sites:     trạng thái gộp của từng siêu thị (1 dòng / siêu thị; data = lan.Site dạng JSON).
--   lan_changes:   lịch sử thiết bị đổi trạng thái, giữ 10 ngày (worker xóa), tối đa 6 lần / thiết bị / ngày.
CREATE TABLE lan_reporters (
    device_id   bigint PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    store_code  text NOT NULL REFERENCES stores(code),
    list_v      text NOT NULL CHECK (list_v ~ '^[0-9a-f]{16}$'),
    received_at timestamptz NOT NULL,
    state       text NOT NULL CHECK (length(state) <= 32),
    lan_ip      inet,
    n           smallint NOT NULL CHECK (n BETWEEN 1 AND 64),
    up          bigint NOT NULL,
    skip        bigint NOT NULL DEFAULT 0,
    dur_ms      integer NOT NULL DEFAULT 0,
    miss        jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(miss) = 'object')
);
CREATE INDEX lan_reporters_store ON lan_reporters (store_code);

CREATE TABLE lan_sites (
    store_code text PRIMARY KEY REFERENCES stores(code),
    data       jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object'),
    updated_at timestamptz NOT NULL
);

CREATE TABLE lan_changes (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    store_code  text NOT NULL,
    target_key  text NOT NULL CHECK (length(target_key) <= 32),
    ip          inet NOT NULL,
    type        text NOT NULL CHECK (length(type) <= 16),
    label       text CHECK (length(label) <= 200),
    from_status text NOT NULL CHECK (length(from_status) <= 16),
    to_status   text NOT NULL CHECK (length(to_status) <= 16),
    at          timestamptz NOT NULL,
    by_device   bigint
);
CREATE INDEX lan_changes_store_at ON lan_changes (store_code, at DESC);
CREATE INDEX lan_changes_at ON lan_changes (at);

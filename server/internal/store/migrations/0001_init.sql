-- 0001: cấu trúc ban đầu.
--
-- Nguyên tắc: ràng buộc nằm trong DB (CHECK, UNIQUE, khóa ngoại) để dù code có lỗi
-- cũng không ghi được dữ liệu sai. Server chỉ NHẬN và LƯU; cảnh báo chỉ hiện trên web.
--
-- Giữ lâu dài : stores, devices, audit
-- Giữ 60 ngày : heartbeats, events (hypertable, Timescale tự xóa theo ngày), device_days, alerts
-- Giữ 1 ngày  : seen_nonces (chống gửi lại gói cũ)
-- Thời gian lưu kiểu timestamptz; "ngày" tính theo giờ Việt Nam (Asia/Ho_Chi_Minh).

CREATE EXTENSION IF NOT EXISTS timescaledb;

-- ---------------------------------------------------------------- siêu thị
CREATE TABLE stores (
    code        text PRIMARY KEY CHECK (code ~ '^[0-9]{1,10}$'),
    -- Mỗi siêu thị một IP WAN cố định. Gán khi admin duyệt máy đầu tiên của siêu thị;
    -- máy gửi từ IP khác thì sinh cảnh báo wan_ip_mismatch.
    wan_ip      inet,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- máy
-- Mọi máy đăng ký đều vào trạng thái pending và chờ admin duyệt.
CREATE TABLE devices (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    hostname       text NOT NULL CHECK (length(hostname) BETWEEN 1 AND 64),  -- như máy gửi lên
    -- Kết quả đọc hostname theo quy ước <mã>-TN<số>[-PRI|-SEC] (internal/fleet).
    -- Sai mẫu: hostname_ok = false, hostname_error ghi lý do, admin xem rồi quyết.
    hostname_ok    boolean NOT NULL,
    hostname_error text CHECK (length(hostname_error) <= 300),
    store_code     text REFERENCES stores(code),
    tn_no          smallint CHECK (tn_no BETWEEN 1 AND 999),
    tn_role        text CHECK (tn_role IN ('PRI', 'SEC')),
    status         text NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending', 'approved', 'rejected', 'revoked')),
    public_key     bytea NOT NULL UNIQUE CHECK (length(public_key) = 32),       -- Ed25519
    fingerprint    text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    fp_components  jsonb NOT NULL,
    os_version     text NOT NULL DEFAULT '' CHECK (length(os_version) <= 128),
    agent_version  text NOT NULL DEFAULT '' CHECK (length(agent_version) <= 64),
    enroll_ip      inet NOT NULL,
    cert_serial    text UNIQUE,
    requested_at   timestamptz NOT NULL DEFAULT now(),
    decided_at     timestamptz,
    decided_by     text CHECK (length(decided_by) <= 80),
    last_seen      timestamptz,
    last_ip        inet,

    -- Hostname hợp lệ thì phải có đủ mã siêu thị + vị trí, và đúng quy ước:
    -- TN1 là PRI, TN2 là SEC, từ TN3 không có hậu tố.
    CONSTRAINT devices_name_parts CHECK (
        NOT hostname_ok OR (
            store_code IS NOT NULL AND tn_no IS NOT NULL AND hostname_error IS NULL AND (
                (tn_no = 1 AND tn_role = 'PRI') OR
                (tn_no = 2 AND tn_role = 'SEC') OR
                (tn_no >= 3 AND tn_role IS NULL)
            )
        )
    ),
    CONSTRAINT devices_name_error CHECK (hostname_ok OR hostname_error IS NOT NULL),
    -- Đã duyệt thì phải biết thuộc siêu thị nào, ai duyệt, lúc nào.
    CONSTRAINT devices_approved CHECK (
        status NOT IN ('approved', 'rejected', 'revoked') OR (decided_at IS NOT NULL AND decided_by IS NOT NULL)
    ),
    CONSTRAINT devices_approved_store CHECK (status <> 'approved' OR store_code IS NOT NULL)
);
-- Trong các máy đang hoạt động: một hostname chỉ một máy, một vị trí TN trong siêu thị chỉ một máy
-- (hai máy cùng xưng 12345-TN1-PRI là clone hoặc đặt nhầm → admin phải xử lý).
CREATE UNIQUE INDEX devices_active_hostname ON devices (upper(hostname)) WHERE status = 'approved';
CREATE UNIQUE INDEX devices_active_position ON devices (store_code, tn_no) WHERE status = 'approved' AND tn_no IS NOT NULL;
CREATE INDEX devices_pending ON devices (requested_at) WHERE status = 'pending';
CREATE INDEX devices_store ON devices (store_code);

-- Trạng thái mới nhất của mỗi máy (ghi đè mỗi heartbeat; fillfactor thấp để cập nhật nhanh).
CREATE TABLE device_status (
    device_id        bigint PRIMARY KEY REFERENCES devices(id),
    received_at      timestamptz NOT NULL,
    agent_time       timestamptz NOT NULL,
    ip               inet NOT NULL,
    uptime_s         bigint NOT NULL CHECK (uptime_s >= 0),
    agent_version    text NOT NULL CHECK (length(agent_version) <= 64),
    rule_set_version text NOT NULL CHECK (length(rule_set_version) <= 64),
    queue_pending    integer NOT NULL CHECK (queue_pending >= 0),
    status           jsonb CHECK (status IS NULL OR pg_column_size(status) <= 16384)
) WITH (fillfactor = 70);

-- ---------------------------------------------------------------- dữ liệu theo ngày (60 ngày)
-- received_at: giờ server nhận (dùng để truy vấn, chia ngày, xóa theo hạn).
-- agent_time : giờ máy gửi tự ghi (hiển thị kèm; đồng hồ máy có thể lệch).
CREATE TABLE heartbeats (
    device_id        bigint NOT NULL REFERENCES devices(id),
    received_at      timestamptz NOT NULL,
    agent_time       timestamptz NOT NULL,
    ip               inet NOT NULL,
    uptime_s         bigint NOT NULL CHECK (uptime_s >= 0),
    agent_version    text NOT NULL CHECK (length(agent_version) <= 64),
    rule_set_version text NOT NULL CHECK (length(rule_set_version) <= 64),
    queue_pending    integer NOT NULL CHECK (queue_pending >= 0),
    status           jsonb CHECK (status IS NULL OR pg_column_size(status) <= 16384)
);
SELECT create_hypertable('heartbeats', by_range('received_at', INTERVAL '1 day'));
CREATE INDEX heartbeats_device_time ON heartbeats (device_id, received_at DESC);

CREATE TABLE events (
    device_id   bigint NOT NULL REFERENCES devices(id),
    received_at timestamptz NOT NULL,
    agent_time  timestamptz NOT NULL,
    rule_id     text NOT NULL CHECK (rule_id ~ '^[A-Za-z0-9._-]{1,64}$'),
    severity    text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    source      text NOT NULL CHECK (length(source) <= 256),
    line        text NOT NULL CHECK (length(line) <= 2048),
    count       integer NOT NULL CHECK (count BETWEEN 1 AND 1000000)
);
SELECT create_hypertable('events', by_range('received_at', INTERVAL '1 day'));
CREATE INDEX events_device_time ON events (device_id, received_at DESC);
CREATE INDEX events_rule_time ON events (rule_id, received_at DESC);
CREATE INDEX events_serious ON events (received_at DESC) WHERE severity IN ('high', 'critical');

-- Nén dữ liệu cũ hơn 2 ngày (gom theo máy), tự xóa sau 60 ngày.
ALTER TABLE heartbeats SET (timescaledb.compress, timescaledb.compress_segmentby = 'device_id',
                            timescaledb.compress_orderby = 'received_at DESC');
ALTER TABLE events SET (timescaledb.compress, timescaledb.compress_segmentby = 'device_id',
                        timescaledb.compress_orderby = 'received_at DESC');
SELECT add_compression_policy('heartbeats', INTERVAL '2 days');
SELECT add_compression_policy('events', INTERVAL '2 days');
SELECT add_retention_policy('heartbeats', INTERVAL '60 days');
SELECT add_retention_policy('events', INTERVAL '60 days');

-- Tóm tắt mỗi máy mỗi ngày (theo giờ Việt Nam): trang xem theo ngày đọc bảng này, không quét heartbeats.
CREATE TABLE device_days (
    device_id   bigint NOT NULL REFERENCES devices(id),
    day         date NOT NULL,
    first_seen  timestamptz NOT NULL,
    last_seen   timestamptz NOT NULL,
    heartbeats  integer NOT NULL DEFAULT 0 CHECK (heartbeats >= 0),
    events      integer NOT NULL DEFAULT 0 CHECK (events >= 0),
    PRIMARY KEY (device_id, day),
    CHECK (last_seen >= first_seen)
);
CREATE INDEX device_days_day ON device_days (day);

-- ---------------------------------------------------------------- chống gửi lại gói cũ
-- Mỗi gói đã ký có nonce ngẫu nhiên. Ghi nonce trước, trùng thì bỏ gói (agent gửi lại do
-- timeout vẫn nhận OK, nhưng dữ liệu không bị ghi hai lần). Cùng transaction với dữ liệu.
CREATE TABLE seen_nonces (
    device_id   bigint NOT NULL REFERENCES devices(id),
    nonce       text NOT NULL CHECK (nonce ~ '^[0-9a-f]{32}$'),
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, nonce)
);
CREATE INDEX seen_nonces_time ON seen_nonces (received_at);

-- ---------------------------------------------------------------- cảnh báo (chỉ hiện trên web)
CREATE TABLE alerts (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id   bigint REFERENCES devices(id),
    store_code  text REFERENCES stores(code),
    kind        text NOT NULL CHECK (kind IN (
                    'enroll_request',       -- máy mới chờ duyệt
                    'hostname_invalid',     -- hostname sai quy ước
                    'wan_ip_mismatch',      -- gửi từ IP khác IP WAN của siêu thị
                    'fingerprint_changed',  -- phần cứng/OS đổi (thay linh kiện hoặc clone)
                    'position_conflict',    -- hai máy cùng vị trí TN trong siêu thị
                    'offline',              -- quá lâu không gửi heartbeat
                    'event'                 -- sự kiện khớp rule mức cao
                )),
    severity    text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    last_at     timestamptz NOT NULL DEFAULT now(),
    occurrences integer NOT NULL DEFAULT 1 CHECK (occurrences >= 1),
    detail      jsonb NOT NULL DEFAULT '{}' CHECK (pg_column_size(detail) <= 8192),
    acked_at    timestamptz,
    acked_by    text CHECK (length(acked_by) <= 80),
    CHECK ((acked_at IS NULL) = (acked_by IS NULL))
);
-- Mỗi máy chỉ một cảnh báo đang mở cho mỗi loại (trừ event): lặp lại thì tăng occurrences,
-- không sinh hàng nghìn dòng giống nhau.
CREATE UNIQUE INDEX alerts_open_one ON alerts (device_id, kind) WHERE acked_at IS NULL AND kind <> 'event';
CREATE INDEX alerts_open ON alerts (created_at DESC) WHERE acked_at IS NULL;
CREATE INDEX alerts_created ON alerts (created_at);

-- ---------------------------------------------------------------- nhật ký thao tác (giữ lâu dài)
-- Duyệt / từ chối / thu hồi máy, xác nhận cảnh báo, đăng ký máy... Chỉ được ghi thêm.
CREATE TABLE audit (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT now(),
    actor       text NOT NULL CHECK (length(actor) BETWEEN 1 AND 80),   -- tên admin / 'system' / 'agent'
    action      text NOT NULL CHECK (action ~ '^[a-z_.]{1,64}$'),
    device_id   bigint REFERENCES devices(id),
    store_code  text,
    detail      jsonb NOT NULL DEFAULT '{}' CHECK (pg_column_size(detail) <= 8192)
);
CREATE INDEX audit_at ON audit (at DESC);
CREATE INDEX audit_device ON audit (device_id, at DESC);

CREATE FUNCTION audit_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'bảng audit chỉ được ghi thêm, không được sửa hay xóa';
END
$$;
CREATE TRIGGER audit_no_update_delete BEFORE UPDATE OR DELETE ON audit
    FOR EACH ROW EXECUTE FUNCTION audit_append_only();
CREATE TRIGGER audit_no_truncate BEFORE TRUNCATE ON audit
    FOR EACH STATEMENT EXECUTE FUNCTION audit_append_only();

-- ---------------------------------------------------------------- dọn dẹp định kỳ (chạy trong DB)
-- heartbeats/events đã có retention policy của Timescale; các bảng thường dọn ở đây, mỗi giờ.
CREATE PROCEDURE scs_cleanup(job_id integer, config jsonb) LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM seen_nonces WHERE received_at < now() - INTERVAL '1 day';
    DELETE FROM device_days WHERE day < (now() AT TIME ZONE 'Asia/Ho_Chi_Minh')::date - 60;
    DELETE FROM alerts WHERE created_at < now() - INTERVAL '60 days';
END
$$;
SELECT add_job('scs_cleanup', INTERVAL '1 hour');

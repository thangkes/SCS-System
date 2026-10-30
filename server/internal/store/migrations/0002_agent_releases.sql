-- 0002: phát hành bản agent từ server và cấu hình agent đặt trên web.
--
-- Agent cài lần đầu là bản "khung" cố định. Sau khi được duyệt, heartbeat cho agent biết bản
-- cần chạy (theo nhóm phát hành) và cấu hình; agent tự tải bản mới, kiểm chữ ký rồi chạy.

-- Các bản agent server đang giữ (mỗi bản một file cho mỗi kiến trúc), ký bằng khóa phát hành.
CREATE TABLE agent_releases (
    version     text NOT NULL CHECK (version ~ '^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$'),
    arch        text NOT NULL CHECK (arch IN ('amd64', 'arm64')),
    sha256      text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size        bigint NOT NULL CHECK (size > 0 AND size <= 67108864),
    sig         text NOT NULL CHECK (length(sig) <= 128),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (version, arch)
);

-- Bản đang phát hành cho từng nhóm: 'test' = nhóm máy thử (admin đánh dấu), 'all' = mọi máy.
-- version NULL = chưa phát hành (máy giữ bản đang chạy). Máy nhóm thử theo 'test' nếu có, không
-- thì theo 'all'.
CREATE TABLE agent_rollout (
    ring        text PRIMARY KEY CHECK (ring IN ('test', 'all')),
    version     text,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  text NOT NULL DEFAULT 'system' CHECK (length(updated_by) BETWEEN 1 AND 80)
);
INSERT INTO agent_rollout (ring) VALUES ('test'), ('all');

ALTER TABLE devices ADD COLUMN test_ring boolean NOT NULL DEFAULT false;

-- Cấu hình agent: chung (scope_key ''), theo siêu thị (mã siêu thị), theo máy (id máy).
-- Giá trị NULL = kế thừa cấp trên. Thứ tự ưu tiên: máy > siêu thị > chung.
CREATE TABLE agent_config (
    scope           text NOT NULL CHECK (scope IN ('global', 'store', 'device')),
    scope_key       text NOT NULL CHECK (
        (scope = 'global' AND scope_key = '') OR
        (scope = 'store' AND scope_key ~ '^[0-9]{1,10}$') OR
        (scope = 'device' AND scope_key ~ '^[1-9][0-9]{0,18}$')),
    watch_services  text[] CHECK (watch_services IS NULL OR cardinality(watch_services) <= 32),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      text NOT NULL CHECK (length(updated_by) BETWEEN 1 AND 80),
    PRIMARY KEY (scope, scope_key)
);

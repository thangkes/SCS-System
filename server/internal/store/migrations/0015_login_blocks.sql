-- Khóa đăng nhập web quản trị theo IP / thiết bị (sống qua khởi động lại server). Bộ đếm lần sai chỉ nằm
-- trong RAM; ở đây chỉ lưu khóa đang hiệu lực. Gỡ khóa / hết hạn thì xóa dòng; mọi lần khóa và gỡ ghi audit.
CREATE TABLE login_blocks (
    kind       text NOT NULL CHECK (kind IN ('ip', 'device')),
    key        text NOT NULL CHECK (length(key) BETWEEN 1 AND 80),
    ip         text NOT NULL DEFAULT '' CHECK (length(ip) <= 45),
    reason     text NOT NULL CHECK (length(reason) <= 300),
    until      timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, key)
);

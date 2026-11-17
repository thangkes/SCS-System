-- Tài khoản web quản trị đăng nhập bằng mã gửi qua email (Gmail). Không lưu mật khẩu, không lưu mã:
-- mã đăng nhập chỉ nằm trong RAM của server, hạn 5 phút, nhập một lần.
-- Mức quyền 1–7 (số nhỏ = quyền cao, mức trên có mọi quyền của mức dưới); mức 0 (quản trị gốc) chỉ có
-- khi đăng nhập bằng mã lấy qua SSH trên máy server, không gán được cho tài khoản.
CREATE TABLE accounts (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      text NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9][a-z0-9._-]{2,31}$'),
    email         text NOT NULL UNIQUE CHECK (length(email) <= 254 AND email ~ '^[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,24}$'),
    status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'disabled')),
    level         smallint CHECK (level BETWEEN 1 AND 7),
    created_at    timestamptz NOT NULL DEFAULT now(),
    approved_at   timestamptz,
    approved_by   text CHECK (length(approved_by) <= 80),
    last_login_at timestamptz,
    CHECK (status <> 'active' OR level IS NOT NULL)
);
CREATE INDEX accounts_pending ON accounts (created_at) WHERE status = 'pending';

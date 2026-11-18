-- Giới hạn phiên đăng nhập theo từng tài khoản (chỉ quản trị gốc đặt): thời gian sống của một phiên
-- (1 giờ – 15 ngày, mặc định 24 giờ) và số phiên mở cùng lúc (1 – 20, mặc định 4).
ALTER TABLE accounts
    ADD COLUMN session_hours integer NOT NULL DEFAULT 24 CHECK (session_hours BETWEEN 1 AND 360),
    ADD COLUMN max_sessions  integer NOT NULL DEFAULT 4  CHECK (max_sessions BETWEEN 1 AND 20);

-- Đăng nhập bằng app xác thực (TOTP) khi không gửi được mail (SMTP bị chặn):
--   totp_secret: khóa bí mật 20 byte ĐÃ MÃ HÓA AES-256-GCM (nonce || bản mã) bằng khóa của server
--                (SCS_TOTP_KEY, file ngoài DB); NULL = chưa cài app.
--   totp_last_step: bước 30 giây của mã đã dùng gần nhất — mã cũ / vừa dùng không dùng lại được.
-- Email không còn bắt buộc: tài khoản chỉ dùng app xác thực có thể không có email (UNIQUE vẫn giữ, NULL không trùng).
ALTER TABLE accounts
    ALTER COLUMN email DROP NOT NULL,
    ADD COLUMN totp_secret     bytea CHECK (length(totp_secret) BETWEEN 40 AND 128),
    ADD COLUMN totp_last_step  bigint NOT NULL DEFAULT 0 CHECK (totp_last_step >= 0),
    ADD COLUMN totp_enabled_at timestamptz;

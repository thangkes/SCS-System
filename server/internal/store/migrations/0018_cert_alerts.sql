-- Server kiểm chứng chỉ máy: thêm hai loại cảnh báo. cert_expiring = chứng chỉ còn dưới 14 ngày mà máy chưa gia hạn
-- (agent bản mới tự gia hạn từ 30 ngày trước hạn); cert_expired = đã hết hạn (máy không gửi được gì nữa). Worker tự mở
-- và tự đóng khi máy gia hạn xong.
ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN (
    'enroll_request', 'hostname_invalid', 'wan_ip_mismatch', 'fingerprint_changed', 'position_conflict',
    'offline', 'event', 'cert_expiring', 'cert_expired'));

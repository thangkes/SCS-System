-- Duyệt tự động máy mới (trang Chờ duyệt): công tắc bật / tắt lưu trong bảng settings,
-- và lý do máy chưa được duyệt tự động (hiện trên trang Chờ duyệt, cập nhật mỗi lần máy hỏi lại).
CREATE TABLE settings (
    key        text PRIMARY KEY CHECK (key ~ '^[a-z_]{1,64}$'),
    value      jsonb NOT NULL,
    updated_by text CHECK (length(updated_by) <= 80),
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE devices
    ADD COLUMN auto_note text CHECK (auto_note IS NULL OR length(auto_note) <= 300);

-- 0003: nhãn ngữ cảnh máy trong cấu hình agent 3 cấp (chung / siêu thị / máy).
--
-- Agent tự suy ra role / store từ hostname và os từ /etc/os-release; nhãn ở đây ghi đè từng khóa
-- (chung < siêu thị < máy) để chọn rule theo applies_to. NULL = cấp này không đặt nhãn.
ALTER TABLE agent_config ADD COLUMN labels jsonb
    CHECK (labels IS NULL OR (jsonb_typeof(labels) = 'object' AND pg_column_size(labels) <= 4096));

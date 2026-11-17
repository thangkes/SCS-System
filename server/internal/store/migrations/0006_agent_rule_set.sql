-- Bộ rule gán cho máy (mốc M7): tên@phiên bản của bộ đã ký trong kho rule, theo cấp chung / siêu thị / máy
-- (cấp cụ thể hơn thắng). Server gửi trong heartbeat, agent tự tải, kiểm chữ ký rồi cài.
ALTER TABLE agent_config ADD COLUMN rule_set text
    CHECK (rule_set IS NULL OR rule_set ~ '^[a-z0-9][a-z0-9._-]{0,63}@[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$');

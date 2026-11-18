-- Thông tin siêu thị nhập trên web (trang Máy → "Thông tin siêu thị"): tên, tổ chức, người phụ trách, ghi chú,
-- và thêm IP WAN (ngoài wan_ip chính) để máy gửi từ các IP này không bị cảnh báo "IP lạ".
ALTER TABLE stores
    ADD COLUMN name              text CHECK (name IS NULL OR length(name) BETWEEN 1 AND 120),
    ADD COLUMN organization      text CHECK (organization IS NULL OR length(organization) BETWEEN 1 AND 80),
    ADD COLUMN extra_ips         inet[] NOT NULL DEFAULT '{}' CHECK (cardinality(extra_ips) <= 8),
    ADD COLUMN manager           text CHECK (manager IS NULL OR length(manager) BETWEEN 1 AND 120),
    ADD COLUMN deputy            text CHECK (deputy IS NULL OR length(deputy) BETWEEN 1 AND 120),
    ADD COLUMN leader            text CHECK (leader IS NULL OR length(leader) BETWEEN 1 AND 120),
    ADD COLUMN deputy_department text CHECK (deputy_department IS NULL OR length(deputy_department) BETWEEN 1 AND 120),
    ADD COLUMN note              text CHECK (note IS NULL OR length(note) <= 2000),
    ADD COLUMN info_updated_by   text CHECK (info_updated_by IS NULL OR length(info_updated_by) <= 80);

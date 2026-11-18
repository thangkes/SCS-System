-- Sửa ràng buộc devices_name_parts (0001): "tn_role = 'PRI'" với tn_role NULL cho kết quả NULL, mà CHECK
-- coi NULL là đạt → DB nhận "12345-TN1" thiếu -PRI. Viết lại bằng IS NOT DISTINCT FROM (NULL so được).
-- NOT VALID: chỉ kiểm dòng thêm / sửa từ nay; dòng cũ đều do server ghi sau khi đã kiểm hostname đúng quy ước.
ALTER TABLE devices DROP CONSTRAINT devices_name_parts;
ALTER TABLE devices ADD CONSTRAINT devices_name_parts CHECK (
    NOT hostname_ok OR (
        store_code IS NOT NULL AND tn_no IS NOT NULL AND hostname_error IS NULL AND (
            (tn_no = 1 AND tn_role IS NOT DISTINCT FROM 'PRI') OR
            (tn_no = 2 AND tn_role IS NOT DISTINCT FROM 'SEC') OR
            (tn_no >= 3 AND tn_role IS NULL)
        )
    )
) NOT VALID;

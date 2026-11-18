-- Tiến độ phát hành bản agent (trang Agent: biểu đồ số máy / % đã lên bản mới theo thời gian).
-- Server ghi 5 phút một dòng khi có bản đang phát hành cho mọi máy; giữ 90 ngày.
CREATE TABLE rollout_progress (
    at        timestamptz PRIMARY KEY,
    version   text NOT NULL CHECK (length(version) BETWEEN 1 AND 64),
    on_target integer NOT NULL CHECK (on_target >= 0),
    total     integer NOT NULL CHECK (total >= 0)
);

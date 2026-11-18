-- Lịch sử thông số máy server, mỗi phút một dòng (trang Báo cáo server). Giữ SCS_METRICS_KEEP (mặc định 90 ngày).
CREATE TABLE server_metrics (
    at          timestamptz PRIMARY KEY,               -- đầu phút
    host        text CHECK (length(host) <= 255),
    samples     smallint NOT NULL CHECK (samples >= 0), -- số mẫu 10 giây trong phút
    cpu_avg     real,                                  -- % (NULL = không đo được)
    cpu_max     real,
    load1       real,
    mem_used    bigint,
    mem_total   bigint,
    disk_used   bigint,
    disk_total  bigint,
    rx_bytes    bigint NOT NULL DEFAULT 0 CHECK (rx_bytes >= 0), -- nhận trong phút, mọi card trừ card ảo
    tx_bytes    bigint NOT NULL DEFAULT 0 CHECK (tx_bytes >= 0),
    rx_peak_bps real,                                  -- byte/giây cao nhất trong phút
    tx_peak_bps real,
    heartbeats  integer NOT NULL DEFAULT 0,
    log_events  integer NOT NULL DEFAULT 0,
    conns_max   integer NOT NULL DEFAULT 0,
    rss         bigint,                                -- RAM tiến trình scs-server
    db_size     bigint                                 -- dung lượng database (byte)
);

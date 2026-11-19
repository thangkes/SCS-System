-- Báo cáo sự cố: tìm giai đoạn cảnh báo / mất liên lạc theo thời điểm bắt đầu trên toàn hệ thống.
CREATE INDEX device_periods_incidents ON device_periods (started_at DESC) WHERE kind <> 'ok';

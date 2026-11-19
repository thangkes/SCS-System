package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationsEmbedded(t *testing.T) {
	list, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 || list[0].Version != 1 || !strings.Contains(list[0].SQL, "CREATE TABLE devices") {
		t.Fatalf("migration nhúng sai: %+v", list)
	}
}

func TestLoadMigrationsRejectsBadSets(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"sai tên":     {"m/1_init.sql": {Data: []byte("x")}},
		"trùng số":    {"m/0001_a.sql": {Data: []byte("x")}, "m/0001_b.sql": {Data: []byte("x")}},
		"thiếu số":    {"m/0001_a.sql": {Data: []byte("x")}, "m/0003_c.sql": {Data: []byte("x")}},
		"chữ hoa tên": {"m/0001_Init.sql": {Data: []byte("x")}},
	}
	for name, fsys := range cases {
		if _, err := loadMigrations(fsys, "m"); err == nil {
			t.Errorf("%s: phải lỗi", name)
		}
	}
	ok := fstest.MapFS{"m/0002_b.sql": {Data: []byte("2")}, "m/0001_a.sql": {Data: []byte("1")}}
	list, err := loadMigrations(ok, "m")
	if err != nil || len(list) != 2 || list[0].Name != "a" || list[1].Version != 2 {
		t.Fatalf("thứ tự sai: %+v %v", list, err)
	}
}

// DB chưa lên: Migrator không làm sập server, /readyz báo chưa sẵn sàng, ctx hủy thì dừng.
func TestMigratorWaitsForDB(t *testing.T) {
	pool, err := Open(context.Background(), "postgres://u:p@127.0.0.1:1/x?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m, err := NewMigrator(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	old := RetryEvery
	RetryEvery = 50 * time.Millisecond
	t.Cleanup(func() { RetryEvery = old })
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run phải trả nil khi ctx hủy: %v", err)
	}
	if err := m.Check().Fn(context.Background()); err == nil {
		t.Fatal("chưa chạy migration thì /readyz phải báo chưa sẵn sàng")
	}
}

// ---------------------------------------------------------------- test với PostgreSQL thật
// Chạy trên máy có Postgres + TimescaleDB (compose.dev.yml), ví dụ:
//
//	SCS_TEST_DATABASE_URL='postgres://scs:<mật khẩu>@127.0.0.1:5432/scs_test?sslmode=disable' go test ./internal/store/
//
// DB test phải là DB riêng có chữ "test" trong tên (test xóa sạch schema public).
// Không đặt biến thì bỏ qua.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("SCS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("chưa đặt SCS_TEST_DATABASE_URL")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	var db string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&db); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db, "test") {
		t.Fatalf("DB %q không có chữ 'test' trong tên; từ chối xóa dữ liệu", db)
	}
	// Bắt đầu sạch: xóa job dọn dẹp và mọi bảng của scs (không DROP SCHEMA public: sẽ xóa
	// luôn extension TimescaleDB, mà tạo lại extension trong cùng phiên dễ lỗi).
	// Xóa hypertable thì Timescale tự bỏ các job nén / xóa theo hạn gắn với nó.
	pool.Exec(ctx, "SELECT delete_job(job_id) FROM timescaledb_information.jobs WHERE proc_name = 'scs_cleanup'")
	// TimescaleDB không cho xóa hypertable chung một lệnh với bảng khác: xóa TỪNG bảng. Lấy danh sách từ
	// pg_tables (mọi bảng trong schema public) thay vì viết cứng: migration mới thêm bảng (ví dụ 0015
	// login_blocks) không bị sót lại làm lần migrate sau báo "already exists".
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+pgx.Identifier{tbl}.Sanitize()+" CASCADE"); err != nil {
			t.Fatalf("xóa bảng %s: %v", tbl, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		DROP PROCEDURE IF EXISTS scs_cleanup(integer, jsonb);
		DROP FUNCTION IF EXISTS audit_append_only()`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func migrateOnce(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	m, err := NewMigrator(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateOnPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	migrateOnce(t, pool)
	migrateOnce(t, pool) // chạy lại không lỗi, không chạy trùng

	var n int
	all, _ := Migrations()
	pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n)
	if n != len(all) {
		t.Fatalf("schema_migrations có %d dòng, muốn %d", n, len(all))
	}
	// 0004 bỏ các bảng ghi từng dòng (heartbeats, events): không còn hypertable và job nén / xóa của chúng.
	pool.QueryRow(ctx, `SELECT count(*) FROM timescaledb_information.hypertables
		WHERE hypertable_name IN ('heartbeats', 'events')`).Scan(&n)
	if n != 0 {
		t.Fatalf("không còn hypertable heartbeats / events, có %d", n)
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM timescaledb_information.jobs
		WHERE proc_name IN ('policy_retention', 'policy_compression', 'scs_cleanup')`).Scan(&n)
	if n != 1 {
		t.Fatalf("chỉ còn job dọn dẹp scs_cleanup, có %d job", n)
	}
	if _, err := pool.Exec(ctx, "CALL scs_cleanup(0, '{}')"); err != nil {
		t.Fatalf("thủ tục dọn dẹp lỗi: %v", err)
	}
}

// Ràng buộc trong DB chặn dữ liệu sai dù code có lỗi.
func TestSchemaConstraints(t *testing.T) {
	pool := testPool(t)
	migrateOnce(t, pool)
	ctx := context.Background()
	exec := func(sql string, args ...any) error {
		_, err := pool.Exec(ctx, sql, args...)
		return err
	}

	if err := exec(`INSERT INTO stores (code, wan_ip) VALUES ('12345', '203.0.113.10')`); err != nil {
		t.Fatal(err)
	}
	key := func(b byte) []byte { k := make([]byte, 32); k[0] = b; return k }
	fp := strings.Repeat("a", 64)
	insertDevice := func(host string, ok bool, store, tn, role any, status string, k byte) error {
		var herr, by any
		if !ok {
			herr = "sai mẫu"
		}
		if status != "pending" {
			by = "Quản trị"
		}
		return exec(`INSERT INTO devices (hostname, hostname_ok, hostname_error, store_code, tn_no, tn_role, status,
			public_key, fingerprint, fp_components, enroll_ip, decided_at, decided_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '{}', '203.0.113.10',
			        CASE WHEN $10::text IS NULL THEN NULL ELSE now() END, $10)`,
			host, ok, herr, store, tn, role, status, key(k), fp, by)
	}

	if err := insertDevice("12345-TN1-PRI", true, "12345", 1, "PRI", "approved", 1); err != nil {
		t.Fatalf("máy hợp lệ: %v", err)
	}
	period := `INSERT INTO device_periods (device_id, kind, started_at, ended_at, severity, closed_by, offline_since)
		VALUES (1, $1, now(), $2, $3, $4, $5)`
	if err := exec(period, "ok", nil, nil, nil, nil); err != nil {
		t.Fatalf("giai đoạn hợp lệ: %v", err)
	}
	bad := map[string]error{
		"TN1 thiếu PRI":              insertDevice("12345-TN1", true, "12345", 1, nil, "pending", 2),
		"TN3 có hậu tố":              insertDevice("12345-TN3-SEC", true, "12345", 3, "SEC", "pending", 3),
		"tên sai mà không ghi lý do": exec(`INSERT INTO devices (hostname, hostname_ok, public_key, fingerprint, fp_components, enroll_ip) VALUES ('x', false, $1, $2, '{}', '1.1.1.1')`, key(4), fp),
		"trùng vị trí TN1 đang dùng": insertDevice("12345-TN1-PRI", true, "12345", 1, "PRI", "approved", 5),
		"duyệt mà không có siêu thị": insertDevice("may-la", false, nil, nil, nil, "approved", 6),
		"trùng public key":           insertDevice("12345-TN3", true, "12345", 3, nil, "pending", 1),
		"siêu thị không tồn tại":     insertDevice("99999-TN3", true, "99999", 3, nil, "pending", 7),
		"hai giai đoạn cùng mở":      exec(period, "alert", nil, "high", nil, nil),
		"loại giai đoạn lạ":          exec(period, "vui", time.Now(), nil, "auto", nil),
		"severity lạ":                exec(period, "alert", time.Now(), "urgent", "auto", nil),
		"đóng mà không ghi ai đóng":  exec(period, "ok", time.Now(), nil, nil, nil),
		"đóng trước khi mở":          exec(period, "ok", time.Now().Add(-time.Hour), nil, "auto", nil),
		"OK mà có mất liên lạc":      exec(period, "ok", time.Now(), nil, "auto", time.Now()),
		"lịch sử không phải object":  exec(`INSERT INTO device_status_changes (device_id, changes) VALUES (1, '[]')`),
		"nonce sai định dạng":        exec(`INSERT INTO seen_nonces (device_id, nonce) VALUES (1, 'abc')`),
		"mã siêu thị có chữ":         exec(`INSERT INTO stores (code) VALUES ('ST1')`),
	}
	for name, err := range bad {
		if err == nil {
			t.Errorf("%s: DB phải từ chối", name)
		}
	}
	// Máy sai hostname vẫn vào được hàng chờ duyệt (admin xem rồi quyết).
	if err := insertDevice("ubuntu-pc", false, nil, nil, nil, "pending", 8); err != nil {
		t.Errorf("máy sai hostname phải vào được hàng chờ: %v", err)
	}
	// Gói gửi lại (cùng nonce) bị chặn.
	nonce := strings.Repeat("b", 32)
	if err := exec(`INSERT INTO seen_nonces (device_id, nonce) VALUES (1, $1)`, nonce); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO seen_nonces (device_id, nonce) VALUES (1, $1)`, nonce); err == nil {
		t.Error("nonce trùng phải bị từ chối")
	}
	// Mỗi máy chỉ một cảnh báo đang mở cho mỗi loại.
	if err := exec(`INSERT INTO alerts (device_id, kind, severity) VALUES (1, 'wan_ip_mismatch', 'high')`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO alerts (device_id, kind, severity) VALUES (1, 'wan_ip_mismatch', 'high')`); err == nil {
		t.Error("cảnh báo cùng loại đang mở phải là một dòng")
	}
	// Audit chỉ ghi thêm.
	if err := exec(`INSERT INTO audit (actor, action, device_id) VALUES ('Quản trị', 'device.approve', 1)`); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"UPDATE audit SET actor = 'khác'", "DELETE FROM audit", "TRUNCATE audit"} {
		if err := exec(sql); err == nil {
			t.Errorf("%s phải bị chặn", sql)
		}
	}
}

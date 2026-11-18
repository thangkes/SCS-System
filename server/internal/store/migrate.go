package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thangkes/SCS-System/server/internal/httpx"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration là một file migrations/NNNN_ten.sql. Chạy theo thứ tự số, mỗi file một
// transaction, chạy rồi thì không chạy lại (ghi trong bảng schema_migrations).
type Migration struct {
	Version int
	Name    string
	SQL     string
}

var migrationName = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.sql$`)

// Migrations đọc các migration nhúng trong binary, kiểm tên và thứ tự.
func Migrations() ([]Migration, error) {
	return loadMigrations(migrationFS, "migrations")
}

func loadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration %q sai tên, phải dạng 0001_ten.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		if other, dup := seen[v]; dup {
			return nil, fmt.Errorf("hai migration cùng số %04d: %s và %s", v, other, e.Name())
		}
		seen[v] = e.Name()
		b, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: v, Name: m[2], SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migration phải đánh số liên tục từ 0001, thiếu số %04d", i+1)
		}
	}
	return out, nil
}

// advisoryLockID: khóa chung để nhiều tiến trình scs-server (ingest, admin, worker chạy riêng)
// không cùng chạy migration một lúc.
const advisoryLockID = 0x6d77675f6d6967 // "scs_mig"

// Migrator cập nhật cấu trúc DB khi server khởi động. DB chưa lên thì thử lại định kỳ
// (server vẫn chạy, /readyz báo chưa sẵn sàng cho tới khi xong).
type Migrator struct {
	pool  *pgxpool.Pool
	log   *slog.Logger
	list  []Migration
	ready atomic.Bool
}

// RetryEvery: thử lại sau bấy lâu khi chưa kết nối được DB.
var RetryEvery = 5 * time.Second

// NewMigrator kiểm danh sách migration nhúng; lỗi ở đây là lỗi lúc build.
func NewMigrator(pool *pgxpool.Pool, log *slog.Logger) (*Migrator, error) {
	list, err := Migrations()
	if err != nil {
		return nil, err
	}
	return &Migrator{pool: pool, log: log, list: list}, nil
}

// ErrSchemaNewer: DB đã chạy migration mới hơn binary này (chạy nhầm bản cũ sau khi đã cập nhật).
var ErrSchemaNewer = errors.New("cấu trúc DB mới hơn phiên bản server này")

// Run chạy migration, thử lại cho tới khi thành công hoặc ctx bị hủy. Lỗi không thể tự
// khỏi (DB mới hơn binary, SQL sai) thì dừng thử và báo lỗi để admin xử lý.
func (m *Migrator) Run(ctx context.Context) error {
	lastLog := time.Time{}
	for {
		applied, err := m.migrate(ctx)
		if err == nil {
			m.ready.Store(true)
			m.log.Info("cấu trúc DB đã cập nhật", "phien_ban", len(m.list), "vua_chay", applied)
			<-ctx.Done()
			return nil
		}
		if errors.Is(err, ErrSchemaNewer) || isSQLError(err) {
			m.log.Error("không cập nhật được cấu trúc DB, cần xử lý tay", "err", err)
			<-ctx.Done() // không thoát tiến trình: web quản trị vẫn mở để xem tình trạng
			return nil
		}
		if time.Since(lastLog) >= time.Minute {
			m.log.Warn("chưa kết nối được DB để cập nhật cấu trúc, sẽ thử lại", "err", err)
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(RetryEvery):
		}
	}
}

// Check dùng cho /readyz: chưa cập nhật xong cấu trúc DB thì chưa nhận dữ liệu.
func (m *Migrator) Check() httpx.Check {
	return httpx.Check{Name: "schema", Fn: func(context.Context) error {
		if !m.ready.Load() {
			return errors.New("cấu trúc DB chưa cập nhật xong")
		}
		return nil
	}}
}

func (m *Migrator) migrate(ctx context.Context) (applied []string, err error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	conn, err := m.pool.Acquire(cctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	if _, err := conn.Exec(cctx, "SELECT pg_advisory_lock($1)", int64(advisoryLockID)); err != nil {
		return nil, err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", int64(advisoryLockID))

	if _, err := conn.Exec(cctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer PRIMARY KEY,
		name       text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}
	done := map[int]bool{}
	rows, err := conn.Query(cctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		done[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for v := range done {
		if v > len(m.list) {
			return nil, fmt.Errorf("%w: DB ở phiên bản %04d, server chỉ biết tới %04d", ErrSchemaNewer, v, len(m.list))
		}
	}

	for _, mg := range m.list {
		if done[mg.Version] {
			continue
		}
		// Deadlock / xung đột tuần tự (ví dụ xóa hypertable đúng lúc tác vụ nền của TimescaleDB đang chạy
		// trên nó): cả transaction đã hủy, thử lại vài lần là qua.
		var err error
		for attempt := 1; ; attempt++ {
			err = m.applyOne(cctx, conn, mg)
			if err == nil || !retryable(err) || attempt >= migrateRetries {
				break
			}
			m.log.Warn("migration gặp deadlock, thử lại", "migration", fmt.Sprintf("%04d_%s", mg.Version, mg.Name), "lan", attempt)
			select {
			case <-cctx.Done():
				return applied, cctx.Err()
			case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
			}
		}
		if err != nil {
			return applied, err
		}
		applied = append(applied, fmt.Sprintf("%04d_%s", mg.Version, mg.Name))
	}
	return applied, nil
}

// migrateRetries: số lần chạy một migration khi gặp deadlock / xung đột tuần tự.
const migrateRetries = 5

// applyOne chạy một file migration và ghi vào schema_migrations trong cùng transaction.
func (m *Migrator) applyOne(ctx context.Context, conn *pgxpool.Conn, mg Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	// Exec không tham số dùng simple protocol: chạy được cả file nhiều câu lệnh.
	if _, err := tx.Exec(ctx, mg.SQL); err != nil {
		tx.Rollback(context.Background())
		return &sqlError{name: fmt.Sprintf("%04d_%s", mg.Version, mg.Name), err: err}
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", mg.Version, mg.Name); err != nil {
		tx.Rollback(context.Background())
		return err
	}
	return tx.Commit(ctx)
}

// retryable: deadlock (40P01) hoặc xung đột tuần tự (40001) — chạy lại transaction là được.
func retryable(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "40P01" || pe.Code == "40001")
}

// sqlError: migration chạy lỗi (SQL sai, thiếu extension...). Thử lại không tự khỏi.
type sqlError struct {
	name string
	err  error
}

func (e *sqlError) Error() string { return "migration " + e.name + ": " + e.err.Error() }
func (e *sqlError) Unwrap() error { return e.err }

func isSQLError(err error) bool {
	var se *sqlError
	return errors.As(err, &se)
}

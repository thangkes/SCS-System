package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/thangkes/SCS-System/server/internal/auth"
)

// Kho tài khoản web quản trị (cài đặt auth.AccountStore). Mọi thay đổi ghi vào bảng audit.

var _ auth.AccountStore = (*Registry)(nil)

const accountCols = `id, username, COALESCE(email, ''), status, COALESCE(level, 0), created_at, approved_at, COALESCE(approved_by, ''), last_login_at,
	session_hours, max_sessions, totp_secret IS NOT NULL`

func scanAccount(row pgx.Row) (auth.Account, error) {
	var a auth.Account
	var lv int16
	var approved, last *time.Time
	if err := row.Scan(&a.ID, &a.Username, &a.Email, &a.Status, &lv, &a.CreatedAt, &approved, &a.ApprovedBy, &last,
		&a.SessionHours, &a.MaxSessions, &a.TOTP); err != nil {
		return a, err
	}
	a.Level = auth.Level(lv)
	if approved != nil {
		a.ApprovedAt = *approved
	}
	if last != nil {
		a.LastLogin = *last
	}
	return a, nil
}

func (g *Registry) AccountByName(ctx context.Context, username string) (auth.Account, error) {
	a, err := scanAccount(g.pool.QueryRow(ctx, `SELECT `+accountCols+` FROM accounts WHERE username = $1`, username))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, auth.ErrNoAccount
	}
	return a, err
}

func (g *Registry) AccountTaken(ctx context.Context, username, email string) (bool, error) {
	var n int
	err := g.pool.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE username = $1 OR (email = $2 AND $2 <> '')`, username, email).Scan(&n)
	return n > 0, err
}

func (g *Registry) CreateAccount(ctx context.Context, a auth.Account) error {
	err := g.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO accounts (username, email, status) VALUES ($1, NULLIF($2, ''), 'pending')`,
			a.Username, a.Email); err != nil {
			return err
		}
		return audit(ctx, tx, a.Username, "account.register", 0, "", map[string]any{"username": a.Username})
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return auth.ErrAccountExists
	}
	return err
}

func (g *Registry) ListAccounts(ctx context.Context) ([]auth.Account, error) {
	rows, err := g.pool.Query(ctx, `SELECT `+accountCols+` FROM accounts
		ORDER BY (status = 'pending') DESC, username LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (g *Registry) UpdateAccount(ctx context.Context, username, status string, level auth.Level, by string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		var old string
		if err := tx.QueryRow(ctx, `SELECT status FROM accounts WHERE username = $1 FOR UPDATE`, username).Scan(&old); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNoAccount
			}
			return err
		}
		var lv any
		if auth.ValidAccountLevel(level) {
			lv = int16(level)
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET status = $2, level = COALESCE($3, level),
			approved_at = CASE WHEN $4 THEN now() ELSE approved_at END,
			approved_by = CASE WHEN $4 THEN $5 ELSE approved_by END
			WHERE username = $1`, username, status, lv, old == auth.AccountPending && status == auth.AccountActive, by); err != nil {
			return err
		}
		return audit(ctx, tx, by, "account.update", 0, "", map[string]any{
			"username": username, "status": status, "old_status": old, "level": int(level)})
	})
}

func (g *Registry) DeleteAccount(ctx context.Context, username, by string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM accounts WHERE username = $1`, username)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrNoAccount
		}
		return audit(ctx, tx, by, "account.delete", 0, "", map[string]any{"username": username})
	})
}

func (g *Registry) TouchLogin(ctx context.Context, username string) error {
	_, err := g.pool.Exec(ctx, `UPDATE accounts SET last_login_at = now() WHERE username = $1`, username)
	return err
}

func (g *Registry) CountPending(ctx context.Context) (int, error) {
	var n int
	err := g.pool.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE status = 'pending'`).Scan(&n)
	return n, err
}

func (g *Registry) SetSessionLimits(ctx context.Context, username string, hours, maxSessions int, by string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE accounts SET session_hours = $2, max_sessions = $3 WHERE username = $1`,
			username, hours, maxSessions)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrNoAccount
		}
		return audit(ctx, tx, by, "account.session_limits", 0, "", map[string]any{
			"username": username, "session_hours": hours, "max_sessions": maxSessions})
	})
}

// ---- app xác thực (cài đặt auth.TOTPStore)

var _ auth.TOTPStore = (*Registry)(nil)

func (g *Registry) TOTPSecret(ctx context.Context, username string) ([]byte, int64, error) {
	var box []byte
	var last int64
	err := g.pool.QueryRow(ctx, `SELECT totp_secret, totp_last_step FROM accounts WHERE username = $1`, username).Scan(&box, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, auth.ErrNoAccount
	}
	return box, last, err
}

func (g *Registry) SetTOTP(ctx context.Context, username string, box []byte, step int64, by string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE accounts SET totp_secret = $2, totp_last_step = $3, totp_enabled_at = now()
			WHERE username = $1`, username, box, step)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrNoAccount
		}
		return audit(ctx, tx, by, "totp.setup", 0, "", map[string]any{"username": username})
	})
}

func (g *Registry) ClearTOTP(ctx context.Context, username, by string) error {
	return g.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE accounts SET totp_secret = NULL, totp_last_step = 0, totp_enabled_at = NULL
			WHERE username = $1`, username)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrNoAccount
		}
		return audit(ctx, tx, by, "totp.reset", 0, "", map[string]any{"username": username})
	})
}

func (g *Registry) UseTOTPStep(ctx context.Context, username string, step int64) (bool, error) {
	tag, err := g.pool.Exec(ctx, `UPDATE accounts SET totp_last_step = $2
		WHERE username = $1 AND totp_secret IS NOT NULL AND totp_last_step < $2`, username, step)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

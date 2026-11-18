package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Migration gặp deadlock / xung đột tuần tự thì chạy lại; lỗi SQL thật thì không.
func TestMigrationRetryable(t *testing.T) {
	dead := &pgconn.PgError{Code: "40P01"}
	cases := map[string]struct {
		err  error
		want bool
	}{
		"deadlock":                {dead, true},
		"deadlock trong sqlError": {&sqlError{name: "0004_x", err: dead}, true},
		"xung đột tuần tự":        {&pgconn.PgError{Code: "40001"}, true},
		"lỗi cú pháp":             {&sqlError{name: "0004_x", err: &pgconn.PgError{Code: "42601"}}, false},
		"lỗi khác":                {errors.New("mất kết nối"), false},
	}
	for name, c := range cases {
		if got := retryable(c.err); got != c.want {
			t.Errorf("%s: %v, muốn %v", name, got, c.want)
		}
	}
}

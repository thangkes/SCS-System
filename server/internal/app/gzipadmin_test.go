package app

import (
	"net/http/httptest"
	"testing"
)

// API quản trị: JSON danh sách được nén; route tài khoản / phiên (chống BREACH) và luồng SSE thì không.
func TestAdminGzip(t *testing.T) {
	d, _ := enrollDeps(t)
	h := AdminHandler(d)
	enc := func(path string) string {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Header().Get("Content-Encoding")
	}
	if e := enc("/admin/v1/devices?status=approved"); e != "gzip" {
		t.Fatalf("danh sách máy phải nén, Content-Encoding=%q", e)
	}
	for _, p := range []string{"/admin/v1/accounts", "/admin/v1/sessions", "/admin/v1/login-blocks"} {
		if e := enc(p); e != "" {
			t.Errorf("%s không được nén (BREACH), Content-Encoding=%q", p, e)
		}
	}
}

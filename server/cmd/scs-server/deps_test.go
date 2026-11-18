package main

import (
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"testing"

	"github.com/thangkes/SCS-System/server/internal/app"
	"github.com/thangkes/SCS-System/server/internal/config"
	"github.com/thangkes/SCS-System/server/internal/httpx"
)

// Mọi trường của base phải tới được handler của từng cổng (lỗi cũ: sót Downloads → cổng agent không
// phân lượt tải, không giới hạn băng thông, web hiện "—" ở ô Đang tải).
func TestHTTPServerPassesAllDeps(t *testing.T) {
	cfg := &config.Config{}
	base := app.Deps{Downloads: app.NewDownloads(4, 20), ReadyChecks: []httpx.Check{}}
	var got app.Deps
	_, err := httpServer("ingest", "127.0.0.1:0", cfg, httpx.NoClientCert, func(d app.Deps) http.Handler {
		got = d
		return http.NotFoundHandler()
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Downloads != base.Downloads || got.Cfg != cfg {
		t.Fatalf("Deps tới handler thiếu trường: Downloads=%p", got.Downloads)
	}
	// Mọi trường con trỏ / interface khác nil trong base phải giữ nguyên (trừ Cfg, Log, Version, Auth do cổng đặt).
	bv, gv := reflect.ValueOf(base), reflect.ValueOf(got)
	for i := 0; i < bv.NumField(); i++ {
		name := bv.Type().Field(i).Name
		switch name {
		case "Cfg", "Log", "Version", "Auth":
			continue
		}
		if !bv.Field(i).IsZero() && gv.Field(i).IsZero() {
			t.Errorf("trường %s bị mất khi tạo cổng", name)
		}
	}
}

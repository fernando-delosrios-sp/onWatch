package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/config"
	"github.com/onllm-dev/onwatch/v2/internal/routing"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

func TestRoutingLoopbackCSRFAndAuthBypass(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	tr := tracker.New(st, slog.Default())
	cfg := &config.Config{Port: 0, AdminUser: "admin", PollInterval: time.Minute}
	h := NewHandler(st, tr, slog.Default(), nil, cfg)
	passHash, err := HashPassword("secret-pass")
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(0, h, slog.Default(), "admin", passHash, "127.0.0.1", "", "", nil, false)
	routingSvc, err := routing.NewService(st.DB(), slog.Default(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv.RegisterRouting(routingSvc.Handler)

	// 1. Direct loopback GET /api/route without session cookie succeeds
	reqGet := httptest.NewRequest(http.MethodGet, "/api/route?harnesses=pi", nil)
	reqGet.RemoteAddr = "127.0.0.1:51234"
	rrGet := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rrGet, reqGet)
	if rrGet.Code != http.StatusOK {
		t.Fatalf("direct loopback GET /api/route: got %d, want 200 (%s)", rrGet.Code, rrGet.Body.String())
	}

	// 2. Direct loopback POST /api/route/feedback without X-Requested-With or session cookie succeeds
	reqPost := httptest.NewRequest(http.MethodPost, "/api/route/feedback", strings.NewReader(`{"pool":"nvidia-free","outcome":"ok"}`))
	reqPost.RemoteAddr = "127.0.0.1:51235"
	rrPost := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rrPost, reqPost)
	if rrPost.Code != http.StatusOK {
		t.Fatalf("direct loopback POST /api/route/feedback: got %d, want 200 (%s)", rrPost.Code, rrPost.Body.String())
	}

	// 3. Non-loopback POST /api/route/feedback without X-Requested-With is blocked (403)
	reqExt := httptest.NewRequest(http.MethodPost, "/api/route/feedback", strings.NewReader(`{"pool":"nvidia-free","outcome":"ok"}`))
	reqExt.RemoteAddr = "203.0.113.10:51235"
	rrExt := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rrExt, reqExt)
	if rrExt.Code != http.StatusForbidden {
		t.Fatalf("non-loopback POST /api/route/feedback without X-Requested-With: got %d, want 403", rrExt.Code)
	}
}

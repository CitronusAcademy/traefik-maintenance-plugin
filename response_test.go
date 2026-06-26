package traefik_maintenance_plugin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	plugin "github.com/CitronusAcademy/traefik-maintenance-plugin"
)

// blockedRequest drives one request through a plugin instance whose only env is
// in maintenance with an empty whitelist, so the request is always blocked.
func blockedRequest(t *testing.T, mutate func(*plugin.Config)) *httptest.ResponseRecorder {
	t.Helper()
	plugin.ResetSharedCacheForTesting()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":true,"whitelist":[]}}}`))
	}))
	t.Cleanup(api.Close)

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": api.URL}
	cfg.EnvironmentSecrets = map[string]plugin.EnvironmentSecret{}
	mutate(cfg)

	h, err := plugin.New(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMaintenanceResponse_DefaultBodyUnchanged(t *testing.T) {
	rec := blockedRequest(t, func(c *plugin.Config) {})
	if got := rec.Body.String(); got != "Service is in maintenance mode" {
		t.Fatalf("default body = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("default content-type = %q", ct)
	}
}

func TestMaintenanceResponse_InlineBody(t *testing.T) {
	rec := blockedRequest(t, func(c *plugin.Config) {
		c.MaintenanceResponseBody = "<h1>Down for maintenance</h1>"
		c.MaintenanceContentType = "text/html; charset=utf-8"
	})
	if got := rec.Body.String(); got != "<h1>Down for maintenance</h1>" {
		t.Fatalf("inline body = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestMaintenanceResponse_FileBodyWinsOverInline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "maintenance.html")
	if err := os.WriteFile(path, []byte("<html>from file</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := blockedRequest(t, func(c *plugin.Config) {
		c.MaintenanceResponseBody = "inline-should-lose"
		c.MaintenanceResponseFilePath = path
		c.MaintenanceContentType = "text/html; charset=utf-8"
	})
	if got := rec.Body.String(); got != "<html>from file</html>" {
		t.Fatalf("file body = %q", got)
	}
}

func TestMaintenanceResponse_UnreadableFileFallsBackToInline(t *testing.T) {
	rec := blockedRequest(t, func(c *plugin.Config) {
		c.MaintenanceResponseFilePath = "/no/such/maintenance/file.html"
		c.MaintenanceResponseBody = "inline-fallback"
	})
	if got := rec.Body.String(); got != "inline-fallback" {
		t.Fatalf("fallback body = %q", got)
	}
}

func TestMaintenanceResponse_RetryAfterEmittedWhenSet(t *testing.T) {
	rec := blockedRequest(t, func(c *plugin.Config) { c.RetryAfterSeconds = 120 })
	if got := rec.Header().Get("Retry-After"); got != "120" {
		t.Fatalf("Retry-After = %q, want 120", got)
	}
}

func TestMaintenanceResponse_NoRetryAfterByDefault(t *testing.T) {
	rec := blockedRequest(t, func(c *plugin.Config) {})
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After should be empty by default, got %q", got)
	}
}

func TestMaintenanceResponse_CacheControlDefaultNoStore(t *testing.T) {
	rec := blockedRequest(t, func(c *plugin.Config) {})
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestMaintenanceResponse_CacheControlOverride(t *testing.T) {
	rec := blockedRequest(t, func(c *plugin.Config) { c.MaintenanceCacheControl = "no-cache, max-age=0" })
	if got := rec.Header().Get("Cache-Control"); got != "no-cache, max-age=0" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestNew_InvalidStatusCodeRejected(t *testing.T) {
	cfg := plugin.CreateConfig()
	cfg.MaintenanceStatusCode = 42 // < 100
	if _, err := plugin.New(context.Background(), nextNoop(), cfg, "test"); err == nil {
		t.Fatal("expected error for out-of-range maintenance status code")
	}
}

func TestNew_InvalidTrustedProxyIgnored(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	cfg := plugin.CreateConfig()
	cfg.Debug = true // exercises the warning branch for the bad entry
	cfg.TrustedProxies = []string{"not-a-cidr", "203.0.113.0/24"}
	if _, err := plugin.New(context.Background(), nextNoop(), cfg, "test"); err != nil {
		t.Fatalf("New should tolerate a bad trustedProxies entry, got %v", err)
	}
}

func TestServeHTTP_SkipHostForwardsDuringMaintenance(t *testing.T) {
	ts, _, activeURL, _, _ := setupTestServer()
	defer ts.Close()
	plugin.ResetSharedCacheForTesting()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": activeURL}
	cfg.EnvironmentSecrets = map[string]plugin.EnvironmentSecret{}
	cfg.SkipHosts = []string{"skip.example.com"}

	forwarded := false
	h, err := plugin.New(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded = true
		w.WriteHeader(http.StatusOK)
	}), cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://skip.example.com/api", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !forwarded || rec.Code != http.StatusOK {
		t.Fatalf("skip host should forward during maintenance: forwarded=%v code=%d", forwarded, rec.Code)
	}
}

func TestServeHTTP_PreflightBlockedWhenNotWhitelisted(t *testing.T) {
	ts, _, activeURL, _, _ := setupTestServer()
	defer ts.Close()
	plugin.ResetSharedCacheForTesting()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": activeURL}
	cfg.EnvironmentSecrets = map[string]plugin.EnvironmentSecret{}

	h, err := plugin.New(context.Background(), nextNoop(), cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodOptions, "http://example.com/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("blocked preflight must be 200 per CORS, got %d", rec.Code)
	}
}

// drive sends one request through a fresh debug-enabled plugin pointed at the
// given endpoint, exercising the verbose logging branches across ServeHTTP, the
// IP/whitelist matcher, and the maintenance/preflight responders.
func drive(t *testing.T, endpoint, method, path, cfIP string, mutate func(*plugin.Config)) *httptest.ResponseRecorder {
	t.Helper()
	plugin.ResetSharedCacheForTesting()
	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": endpoint}
	cfg.EnvironmentSecrets = map[string]plugin.EnvironmentSecret{}
	cfg.Debug = true
	if mutate != nil {
		mutate(cfg)
	}
	h, err := plugin.New(context.Background(), nextNoop(), cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(method, path, nil)
	if cfIP != "" {
		req.Header.Set("Cf-Connecting-Ip", cfIP)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServeHTTP_DebugBranches(t *testing.T) {
	ts, _, activeURL, wildcardURL, specificURL := setupTestServer()
	defer ts.Close()

	// blocked (empty whitelist) — exercises blocked + maintenance-response debug logs
	if rec := drive(t, activeURL, http.MethodGet, "http://example.com/api", "203.0.113.9", nil); rec.Code != 512 {
		t.Fatalf("blocked: code=%d", rec.Code)
	}
	// wildcard allows — exercises wildcard debug branch
	if rec := drive(t, wildcardURL, http.MethodGet, "http://example.com/api", "203.0.113.9", nil); rec.Code != http.StatusOK {
		t.Fatalf("wildcard allow: code=%d", rec.Code)
	}
	// specific IP matches — exercises whitelist-match debug branch
	if rec := drive(t, specificURL, http.MethodGet, "http://example.com/api", "192.168.1.1", nil); rec.Code != http.StatusOK {
		t.Fatalf("specific-ip allow: code=%d", rec.Code)
	}
	// static asset passthrough — exercises static-asset debug branch
	if rec := drive(t, activeURL, http.MethodGet, "http://example.com/app.js", "203.0.113.9", nil); rec.Code != http.StatusOK {
		t.Fatalf("static asset: code=%d", rec.Code)
	}
	// blocked preflight with debug — exercises sendBlockedPreflightResponse debug
	if rec := drive(t, activeURL, http.MethodOptions, "http://example.com/", "203.0.113.9", func(c *plugin.Config) {
		c.AllowedOrigins = []string{"https://app.example.com"}
	}); rec.Code != http.StatusOK {
		t.Fatalf("blocked preflight: code=%d", rec.Code)
	}
}

func TestServeHTTP_HTMLPassthroughDebug(t *testing.T) {
	ts, _, activeURL, _, _ := setupTestServer()
	defer ts.Close()
	plugin.ResetSharedCacheForTesting()
	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": activeURL}
	cfg.EnvironmentSecrets = map[string]plugin.EnvironmentSecret{}
	cfg.Debug = true
	cfg.AllowHTMLWhenMaintenance = true
	h, err := plugin.New(context.Background(), nextNoop(), cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/page", nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTML passthrough should forward, got %d", rec.Code)
	}
}

func TestServeHTTP_CIDRWhitelistAllows(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":true,"whitelist":["10.0.0.0/8"]}}}`))
	}))
	defer api.Close()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": api.URL}
	cfg.EnvironmentSecrets = map[string]plugin.EnvironmentSecret{}
	cfg.Debug = true
	h, err := plugin.New(context.Background(), nextNoop(), cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// in-range IP is allowed (covers isCIDRMatch true + CIDR debug branch)
	in := httptest.NewRequest(http.MethodGet, "http://example.com/api", nil)
	in.Header.Set("Cf-Connecting-Ip", "10.1.2.3")
	recIn := httptest.NewRecorder()
	h.ServeHTTP(recIn, in)
	if recIn.Code != http.StatusOK {
		t.Fatalf("in-range CIDR client should pass, got %d", recIn.Code)
	}

	// out-of-range IP is blocked (covers isCIDRMatch false branch)
	out := httptest.NewRequest(http.MethodGet, "http://example.com/api", nil)
	out.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	recOut := httptest.NewRecorder()
	h.ServeHTTP(recOut, out)
	if recOut.Code != 512 {
		t.Fatalf("out-of-range client should be blocked, got %d", recOut.Code)
	}
}

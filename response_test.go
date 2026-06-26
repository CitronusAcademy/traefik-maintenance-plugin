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

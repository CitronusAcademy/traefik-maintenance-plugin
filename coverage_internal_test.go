package traefik_maintenance_plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestNewDoesNotLeakGoroutinePerInstance(t *testing.T) {
	ResetSharedCacheForTesting()
	defer ResetSharedCacheForTesting()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":false,"whitelist":[]}}}`))
	}))
	defer ts.Close()

	cfg := CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL}

	// First New initializes the singleton; let warmup settle.
	if _, err := New(context.Background(), nopHandler(), cfg, "test"); err != nil {
		t.Fatalf("New: %v", err)
	}
	runtime.GC()
	before := runtime.NumGoroutine()

	// context.Background() is never cancelled, so a per-instance goroutine that
	// blocks on ctx.Done() would never exit — modelling Traefik reusing a
	// long-lived context across config reloads.
	for i := 0; i < 50; i++ {
		if _, err := New(context.Background(), nopHandler(), cfg, "test"); err != nil {
			t.Fatalf("New[%d]: %v", i, err)
		}
	}
	runtime.GC()
	after := runtime.NumGoroutine()

	if after-before > 5 { // allow scheduler slack; 50 leaked goroutines would be >> 5
		t.Fatalf("New leaked goroutines: before=%d after=%d", before, after)
	}
}

func TestLogRequestHeadersForDebugging(t *testing.T) {
	// debug on: exercises the logging branch (output goes to stdout; we only
	// assert it does not panic and the debug-off early return is also covered).
	on := &MaintenanceCheck{debug: true}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Authorization", "Bearer x")
	on.logRequestHeadersForDebugging(req)

	off := &MaintenanceCheck{debug: false}
	off.logRequestHeadersForDebugging(req)
}

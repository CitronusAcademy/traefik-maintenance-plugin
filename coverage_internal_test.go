package traefik_maintenance_plugin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/logx"
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
	orig := logx.Out
	defer func() { logx.Out = orig }()
	var buf bytes.Buffer
	logx.Out = &buf

	on := &MaintenanceCheck{
		debug: true,
		sensitiveHeaders: map[string]struct{}{
			"Authorization":   {},
			"Cookie":          {},
			"X-Plugin-Secret": {},
		},
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Authorization", "Bearer supersecret")
	req.Header.Set("Cookie", "session=abc123")
	req.Header.Set("X-Plugin-Secret", "topsecret")
	req.Header.Set("X-Request-Id", "req-123")
	on.logRequestHeadersForDebugging(req)

	out := buf.String()
	for _, leaked := range []string{"Bearer supersecret", "session=abc123", "topsecret"} {
		if strings.Contains(out, leaked) {
			t.Errorf("sensitive value leaked into debug log: %q\noutput:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("expected [REDACTED] placeholder in debug log, got:\n%s", out)
	}
	// Non-sensitive header value is preserved verbatim.
	if !strings.Contains(out, "req-123") {
		t.Errorf("non-sensitive header value should be logged verbatim, got:\n%s", out)
	}

	// debug off → no output at all.
	buf.Reset()
	off := &MaintenanceCheck{debug: false}
	off.logRequestHeadersForDebugging(req)
	if buf.Len() != 0 {
		t.Errorf("debug off must produce no output, got %q", buf.String())
	}
}

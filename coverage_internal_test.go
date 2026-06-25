package traefik_maintenance_plugin

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStartupWarnsOnMissingSecret(t *testing.T) {
	ResetSharedCacheForTesting()
	defer ResetSharedCacheForTesting()

	out := captureStdout(t, func() {
		// No per-env secret value, no top-level secret value → unprotected env.
		ensureSharedCacheInitialized(
			map[string]string{"": "http://unused.invalid/"},
			map[string]EnvironmentSecret{"": {Header: "X-Plugin-Secret", Value: ""}},
			60*time.Second, 5*time.Second, false, "ua", "", "",
		)
	})
	if !strings.Contains(out, "no secret") {
		t.Fatalf("expected a missing-secret warning, got: %q", out)
	}
}

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

func TestIPVersionName(t *testing.T) {
	if got := ipVersionName(net.ParseIP("1.2.3.4")); got != "IPv4" {
		t.Errorf("IPv4: got %q", got)
	}
	if got := ipVersionName(net.ParseIP("2001:db8::1")); got != "IPv6" {
		t.Errorf("IPv6: got %q", got)
	}
}

func TestCalculateBackoff(t *testing.T) {
	if d := calculateBackoff(0); d != 5*time.Second {
		t.Errorf("attempts=0: got %v, want 5s", d)
	}
	if d := calculateBackoff(-3); d != 5*time.Second {
		t.Errorf("negative attempts: got %v, want 5s", d)
	}
	// attempts=1 → base 10s ± 20% jitter.
	if d := calculateBackoff(1); d < 8*time.Second || d > 12*time.Second {
		t.Errorf("attempts=1: got %v, want within [8s,12s]", d)
	}
	// Large attempts are capped at 1h.
	if d := calculateBackoff(50); d != time.Hour {
		t.Errorf("attempts=50: got %v, want 1h cap", d)
	}
}

func TestCleanIPAddressForms(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":           "1.2.3.4",
		"1.2.3.4:8080":      "1.2.3.4",
		"[2001:db8::1]:443": "2001:db8::1",
		"[2001:db8::1]":     "2001:db8::1",
		"2001:db8::1":       "2001:db8::1",
		"  1.2.3.4  ":       "1.2.3.4",
		"":                  "",
	}
	for in, want := range cases {
		if got := cleanIPAddress(in); got != want {
			t.Errorf("cleanIPAddress(%q) = %q, want %q", in, got, want)
		}
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

func TestIsClientAllowedCIDRAndExact(t *testing.T) {
	m := &MaintenanceCheck{}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Cf-Connecting-Ip", "10.0.0.5")

	if !m.isClientAllowed(req, []string{"10.0.0.0/16"}) {
		t.Error("IP within CIDR must be allowed")
	}
	if m.isClientAllowed(req, []string{"192.168.0.0/16"}) {
		t.Error("IP outside CIDR must be blocked")
	}
	if m.isClientAllowed(req, []string{"not-a-cidr/xx"}) {
		t.Error("invalid CIDR entry must not allow")
	}
	if !m.isClientAllowed(req, []string{"10.0.0.5"}) {
		t.Error("exact IP must be allowed")
	}
	if m.isClientAllowed(req, []string{}) {
		t.Error("empty whitelist must block")
	}
}

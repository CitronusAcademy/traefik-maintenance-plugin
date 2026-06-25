package traefik_maintenance_plugin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	plugin "github.com/CitronusAcademy/traefik-maintenance-plugin"
)

func TestSecretHeaderFunctionality(t *testing.T) {
	// Reset shared state between tests
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	// Track received headers to verify plugin sends the secret header
	var receivedHeaders http.Header
	var headerMutex sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Store received headers for verification
		headerMutex.Lock()
		receivedHeaders = r.Header.Clone()
		headerMutex.Unlock()

		response := maintenanceResponse{}

		// Check for secret header and respond accordingly
		secretHeader := r.Header.Get("X-Plugin-Secret")
		if secretHeader == "test-secret-token" {
			// Plugin request - return full info with whitelist
			response.SystemConfig.Maintenance.IsActive = true
			response.SystemConfig.Maintenance.Whitelist = []string{"192.168.1.1"}
		} else {
			// Frontend request - return only status without whitelist
			response.SystemConfig.Maintenance.IsActive = true
			// No whitelist for frontend
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	// Test with secret header configured
	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL}
	cfg.EnvironmentSecrets = map[string]plugin.EnvironmentSecret{
		"": {Header: "X-Plugin-Secret", Value: "test-secret-token"},
	}
	cfg.CacheDurationInSeconds = 10
	cfg.RequestTimeoutInSeconds = 5
	cfg.MaintenanceStatusCode = 503
	cfg.Debug = false

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	handler, err := plugin.New(context.Background(), next, cfg, "secret-header-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for initial fetch to complete
	time.Sleep(200 * time.Millisecond)

	// Verify that the secret header was sent to the API
	headerMutex.Lock()
	receivedSecret := receivedHeaders.Get("X-Plugin-Secret")
	headerMutex.Unlock()

	if receivedSecret != "test-secret-token" {
		t.Errorf("Expected secret header 'test-secret-token', got '%s'", receivedSecret)
	}

	// Test that the plugin correctly processed the response with whitelist
	req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	req.Header.Set("Cf-Connecting-Ip", "192.168.1.1") // Should be allowed

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Errorf("Expected status code 200 for whitelisted IP, got %d", response.StatusCode)
	}

	// Test that non-whitelisted IP is blocked
	req2 := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	req2.Header.Set("Cf-Connecting-Ip", "10.0.0.1") // Should be blocked

	recorder2 := httptest.NewRecorder()
	handler.ServeHTTP(recorder2, req2)

	response2 := recorder2.Result()
	defer response2.Body.Close()

	if response2.StatusCode != 503 {
		t.Errorf("Expected status code 503 for non-whitelisted IP, got %d", response2.StatusCode)
	}
}

func TestTopLevelSecretUsedWhenPerSuffixValueEmpty(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()

	// Capture via a mutex-guarded variable rather than a channel send inside the
	// handler: Yaegi (Traefik's interpreter) panics on a select/channel-send in an
	// interpreted handler invoked from compiled net/http, but handles this pattern.
	var mu sync.Mutex
	var gotSecret string
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if !called {
			gotSecret = r.Header.Get("X-Plugin-Secret")
			called = true
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":true,"whitelist":[]}}}`))
	}))
	defer srv.Close()

	cfg := plugin.CreateConfig()
	cfg.MaintenanceStatusCode = 512
	cfg.CacheDurationInSeconds = 30
	cfg.RequestTimeoutInSeconds = 5
	cfg.EnvironmentEndpoints = map[string]string{".world": srv.URL}
	// Per-suffix entry exists but carries no value (the CreateConfig default shape).
	cfg.EnvironmentSecrets = map[string]plugin.EnvironmentSecret{
		".world": {Header: "X-Plugin-Secret", Value: ""},
	}
	// The real secret is wired at top level.
	cfg.SecretHeader = "X-Plugin-Secret"
	cfg.SecretHeaderValue = "real-secret-token"

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	if _, err := plugin.New(context.Background(), next, cfg, "test"); err != nil {
		t.Fatalf("New: %v", err)
	}

	// Wait for the background refresher to hit the API.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := called
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	s, done := gotSecret, called
	mu.Unlock()
	if !done {
		t.Fatal("maintenance API was never called")
	}
	if s != "real-secret-token" {
		t.Fatalf("maintenance API received secret %q, want %q", s, "real-secret-token")
	}
}

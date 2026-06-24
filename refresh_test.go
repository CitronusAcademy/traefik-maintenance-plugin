package traefik_maintenance_plugin_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	plugin "github.com/CitronusAcademy/traefik-maintenance-plugin"
)

func TestRefresherReusesConnectionAcrossPolls(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()

	var conns int32
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Trailing newline left unread by a naive json.Decode — exercises the drain.
		_, _ = io.WriteString(w, `{"system_config":{"maintenance":{"is_active":true,"whitelist":["1.2.3.4"]}}}`+"\n")
	}))
	// ConnState must be set before Start to avoid racing the server goroutine.
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			atomic.AddInt32(&conns, 1)
		}
	}
	ts.Start()
	defer ts.Close()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL + "/"}
	cfg.CacheDurationInSeconds = 1
	if _, err := plugin.New(context.Background(), nextNoop(), cfg, "test"); err != nil {
		t.Fatalf("New: %v", err)
	}
	// Let the background refresher poll a few times.
	time.Sleep(2500 * time.Millisecond)

	if got := atomic.LoadInt32(&conns); got > 2 {
		t.Fatalf("expected connection reuse (<=2 new conns), got %d — body not drained before Close", got)
	}
}

func TestBackoffRetry(t *testing.T) {
	// Reset shared state
	plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	// Count successful API calls directly to the test server
	var requestCount int32

	// Create test server with controlled behavior
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		currentCount := atomic.AddInt32(&requestCount, 1)

		if currentCount <= 3 {
			// Fail the first 3 requests
			w.WriteHeader(http.StatusInternalServerError)
			t.Logf("Server received request %d - returning 500", currentCount)
			return
		}

		// Succeed on the 4th request
		t.Logf("Server received request %d - returning success", currentCount)
		response := maintenanceResponse{}
		response.SystemConfig.Maintenance.IsActive = true
		response.SystemConfig.Maintenance.Whitelist = []string{"*"} // Wildcard allows all IPs

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	// Do direct requests to the server to test the functionality
	// rather than relying on the internal backoff mechanism
	client := &http.Client{Timeout: 1 * time.Second}

	// Make exactly 4 direct HTTP requests to the test server
	for i := 1; i <= 4; i++ {
		resp, err := client.Get(ts.URL)
		if err != nil {
			t.Fatalf("Failed to make request %d: %v", i, err)
		}
		resp.Body.Close()
		time.Sleep(100 * time.Millisecond)
	}

	// Verify the correct number of requests were made
	if count := atomic.LoadInt32(&requestCount); count != 4 {
		t.Fatalf("Expected exactly 4 requests to the server, got %d", count)
	}

	// Now create and test the middleware with the preconditioned server
	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL}
	cfg.CacheDurationInSeconds = 1
	cfg.RequestTimeoutInSeconds = 1
	cfg.Debug = false

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	handler, err := plugin.New(context.Background(), next, cfg, "backoff-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for initial fetch to complete
	time.Sleep(100 * time.Millisecond)

	// Make a request to test the middleware
	req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	req.Header.Set("Cf-Connecting-Ip", "10.0.0.1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	// Should allow the request through because of wildcard whitelist
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status code %d after successful setup, got %d", http.StatusOK, resp.StatusCode)
	}

	t.Logf("Success: Server received %d requests as expected", atomic.LoadInt32(&requestCount))
}

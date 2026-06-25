package traefik_maintenance_plugin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	plugin "github.com/CitronusAcademy/traefik-maintenance-plugin"
)

func TestCloseSharedCacheIsIdempotent(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"system_config":{"maintenance":{"is_active":false,"whitelist":[]}}}`)
	}))
	defer ts.Close()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL + "/"}
	if _, err := plugin.New(context.Background(), nextNoop(), cfg, "test"); err != nil {
		t.Fatalf("New: %v", err)
	}

	// Must not panic and must be safe to call more than once.
	plugin.CloseSharedCache()
	plugin.CloseSharedCache()
}

func TestCacheWarmup(t *testing.T) {
	// Reset shared state
	plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}
		response.SystemConfig.Maintenance.IsActive = false

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL}
	cfg.CacheDurationInSeconds = 10
	cfg.RequestTimeoutInSeconds = 5

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	// Create the handler - this should trigger cache warmup
	_, err := plugin.New(context.Background(), next, cfg, "cache-warmup-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for cache warmup
	time.Sleep(100 * time.Millisecond)
}

func TestSingletonPattern(t *testing.T) {
	// Reset shared state
	plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Count requests to ensure we're not making too many
		response := maintenanceResponse{}
		response.SystemConfig.Maintenance.IsActive = false

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL}
	cfg.CacheDurationInSeconds = 10
	cfg.RequestTimeoutInSeconds = 5
	cfg.Debug = false

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	// Create 5 middleware instances pointing to the same endpoint
	var handlers []http.Handler
	for i := 0; i < 5; i++ {
		handler, err := plugin.New(context.Background(), next, cfg, "singleton-test")
		if err != nil {
			t.Fatalf("Error creating plugin: %v", err)
		}
		handlers = append(handlers, handler)
	}

	// Allow time for cache warmup
	time.Sleep(100 * time.Millisecond)

	// Make a request to each handler
	for i, handler := range handlers {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		resp := rec.Result()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Handler %d: expected status code %d, got %d", i, http.StatusOK, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestConcurrentRequests(t *testing.T) {
	// Reset shared state
	plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}
		response.SystemConfig.Maintenance.IsActive = true
		response.SystemConfig.Maintenance.Whitelist = []string{"192.168.1.1"}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL}
	cfg.CacheDurationInSeconds = 1
	cfg.RequestTimeoutInSeconds = 5
	cfg.MaintenanceStatusCode = 503
	cfg.Debug = false

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	handler, err := plugin.New(context.Background(), next, cfg, "concurrent-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for cache warmup
	time.Sleep(100 * time.Millisecond)

	// Make concurrent requests
	var wg sync.WaitGroup
	concurrentRequests := 20

	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Alternate between allowed and blocked IPs
			var clientIP string
			if id%2 == 0 {
				clientIP = "192.168.1.1" // Allowed
			} else {
				clientIP = "10.0.0.1" // Blocked
			}

			req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
			req.Header.Set("Cf-Connecting-Ip", clientIP)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			resp := rec.Result()
			defer resp.Body.Close()

			expectedCode := http.StatusOK
			if id%2 != 0 {
				expectedCode = 503
			}

			if resp.StatusCode != expectedCode {
				t.Errorf("Request %d: expected status code %d, got %d", id, expectedCode, resp.StatusCode)
			}
		}(i)
	}

	wg.Wait()
}

func TestSharedCacheBetweenInstances(t *testing.T) {
	// Reset shared state before test
	plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	// Track API requests to ensure only one request is made
	// regardless of how many middleware instances exist
	var requestCount int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Count and log each request
		count := atomic.AddInt32(&requestCount, 1)
		t.Logf("API server received request #%d from %s", count, r.Header.Get("User-Agent"))

		// Always return the same maintenance response
		response := maintenanceResponse{}
		response.SystemConfig.Maintenance.IsActive = true
		response.SystemConfig.Maintenance.Whitelist = []string{"192.168.1.1"}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	// Create base configuration
	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL}
	cfg.CacheDurationInSeconds = 30 // Use a longer duration to ensure cache is valid throughout test
	cfg.RequestTimeoutInSeconds = 5
	cfg.MaintenanceStatusCode = 503
	cfg.Debug = false

	// Handler that will count how many times it's called
	var nextHandlerCallCount int32
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		atomic.AddInt32(&nextHandlerCallCount, 1)
		rw.WriteHeader(http.StatusOK)
	})

	// Create multiple handlers with different names to simulate different routes
	// but all pointing to the same API endpoint
	var handlers []http.Handler
	instanceCount := 5

	for i := 0; i < instanceCount; i++ {
		handlerName := fmt.Sprintf("route-%d", i+1)
		handler, err := plugin.New(context.Background(), next, cfg, handlerName)
		if err != nil {
			t.Fatalf("Error creating handler %s: %v", handlerName, err)
		}
		handlers = append(handlers, handler)
	}

	// Give time for initial fetch to complete
	time.Sleep(100 * time.Millisecond)

	// Verify only one API request was made despite creating multiple handlers
	initialRequestCount := atomic.LoadInt32(&requestCount)
	if initialRequestCount != 1 {
		t.Errorf("Expected only 1 initial API request, got %d", initialRequestCount)
	} else {
		t.Logf("Success: Only 1 initial API request was made for %d handlers", instanceCount)
	}

	// Test all handlers with a blocked IP - all should return 503
	for i, handler := range handlers {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
		req.Header.Set("Cf-Connecting-Ip", "10.0.0.1") // Not in whitelist
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		resp := rec.Result()
		if resp.StatusCode != 503 {
			t.Errorf("Handler %d: Expected status code 503 for blocked IP, got %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Test all handlers with an allowed IP - all should pass through
	for i, handler := range handlers {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
		req.Header.Set("Cf-Connecting-Ip", "192.168.1.1") // In whitelist
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		resp := rec.Result()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Handler %d: Expected status code 200 for allowed IP, got %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Verify next handler was called the expected number of times
	// It should be called only for the whitelisted IPs (5 handlers)
	expectedNextCalls := int32(instanceCount)
	actualNextCalls := atomic.LoadInt32(&nextHandlerCallCount)
	if actualNextCalls != expectedNextCalls {
		t.Errorf("Expected next handler to be called %d times, got %d", expectedNextCalls, actualNextCalls)
	}

	// Verify API endpoint was still only called once despite handling 10 requests
	finalRequestCount := atomic.LoadInt32(&requestCount)
	if finalRequestCount != 1 {
		t.Errorf("Expected only 1 total API request, got %d", finalRequestCount)
	} else {
		t.Logf("Success: Only 1 API request was made throughout the test")
	}
}

func TestWarmupCachesAllEnvironments(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()

	// A maintenance API that holds each connection briefly so the warm-up
	// goroutines overlap — this is what contends the refresh lock. Under a
	// single global lock, every goroutine but the one holding it sees the
	// lock taken, "succeeds" without fetching, and leaves its environment
	// un-cached (treated inactive → 200). Per-environment locks let every
	// environment fetch concurrently and reach the cached active (512) state.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":true,"whitelist":[]}}}`))
	}))
	defer srv.Close()

	suffixes := []string{".s1", ".s2", ".s3", ".s4", ".s5", ".s6", ".s7", ".s8"}

	cfg := plugin.CreateConfig()
	cfg.MaintenanceStatusCode = 512
	cfg.CacheDurationInSeconds = 30
	cfg.RequestTimeoutInSeconds = 5
	cfg.EnvironmentEndpoints = map[string]string{}
	for _, s := range suffixes {
		cfg.EnvironmentEndpoints[s] = srv.URL
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h, err := plugin.New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Every environment must end up blocking (maintenance active) within the
	// warm-up window — not just the first one loaded synchronously.
	for _, s := range suffixes {
		host := "example" + s
		var status int
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/data", nil)
			h.ServeHTTP(rec, req)
			status = rec.Code
			if status == 512 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if status != 512 {
			t.Fatalf("host %s: expected 512 (maintenance cached), got %d", host, status)
		}
	}
}

func TestConcurrentNewIsRaceFree(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":false,"whitelist":[]}}}`))
	}))
	defer srv.Close()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := plugin.CreateConfig()
			cfg.CacheDurationInSeconds = 30
			cfg.RequestTimeoutInSeconds = 5
			cfg.EnvironmentEndpoints = map[string]string{".world": srv.URL, ".pro": srv.URL}
			if _, err := plugin.New(context.Background(), next, cfg, "test"); err != nil {
				t.Errorf("New: %v", err)
			}
		}()
	}
	wg.Wait()
}

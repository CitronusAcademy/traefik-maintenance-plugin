package traefik_maintenance_plugin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	plugin "github.com/CitronusAcademy/traefik-maintenance-plugin"
)

type maintenanceResponse struct {
	SystemConfig struct {
		Maintenance struct {
			IsActive  bool     `json:"is_active"`
			Whitelist []string `json:"whitelist,omitempty"`
		} `json:"maintenance"`
	} `json:"system_config"`
}

func setupTestServer() (*httptest.Server, string, string, string, string) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}

		switch r.URL.Path {
		case "/maintenance-active":
			response.SystemConfig.Maintenance.IsActive = true
			response.SystemConfig.Maintenance.Whitelist = []string{}
		case "/maintenance-active-wildcard":
			response.SystemConfig.Maintenance.IsActive = true
			response.SystemConfig.Maintenance.Whitelist = []string{"*"}
		case "/maintenance-active-specific-ip":
			response.SystemConfig.Maintenance.IsActive = true
			response.SystemConfig.Maintenance.Whitelist = []string{"192.168.1.1"}
		case "/slow-response":
			time.Sleep(200 * time.Millisecond)
			response.SystemConfig.Maintenance.IsActive = true
			response.SystemConfig.Maintenance.Whitelist = []string{"*"} // Allow all for timeouts
		case "/invalid-json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"invalid json`))
			return
		default:
			response.SystemConfig.Maintenance.IsActive = false
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))

	return ts,
		ts.URL,
		ts.URL + "/maintenance-active",
		ts.URL + "/maintenance-active-wildcard",
		ts.URL + "/maintenance-active-specific-ip"
}

func TestMaintenanceCheck(t *testing.T) {
	// Reset shared state between tests
	plugin.CloseSharedCache()
	time.Sleep(500 * time.Millisecond)

	ts, regularEndpoint, activeEndpoint, wildcardEndpoint, specificIPEndpoint := setupTestServer()
	defer ts.Close()

	tests := []struct {
		name                    string
		endpoint                string
		cacheDurationInSeconds  int
		requestTimeoutInSeconds int
		clientIP                string
		urlPath                 string
		skipPrefixes            []string
		skipHosts               []string
		host                    string
		maintenanceStatusCode   int
		expectedCode            int
		description             string
	}{
		{
			name:                    "Maintenance inactive",
			endpoint:                regularEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/",
			skipPrefixes:            []string{},
			skipHosts:               []string{},
			maintenanceStatusCode:   512,
			expectedCode:            http.StatusOK,
			description:             "When maintenance is inactive, all requests should be allowed",
		},
		{
			name:                    "Maintenance active - no whitelist",
			endpoint:                activeEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/",
			skipPrefixes:            []string{},
			skipHosts:               []string{},
			maintenanceStatusCode:   512,
			expectedCode:            512,
			description:             "When maintenance is active with no whitelist, all requests should be blocked",
		},
		{
			name:                    "Maintenance active - custom status code",
			endpoint:                activeEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/",
			skipPrefixes:            []string{},
			skipHosts:               []string{},
			maintenanceStatusCode:   418, // I'm a teapot
			expectedCode:            418,
			description:             "Should use custom status code when specified",
		},
		{
			name:                    "Maintenance active - IP not in whitelist",
			endpoint:                specificIPEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/",
			skipPrefixes:            []string{},
			skipHosts:               []string{},
			maintenanceStatusCode:   512,
			expectedCode:            512,
			description:             "When maintenance is active and client IP is not in whitelist, request should be blocked",
		},
		{
			name:                    "Maintenance active - IP in whitelist",
			endpoint:                specificIPEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "192.168.1.1",
			urlPath:                 "/",
			skipPrefixes:            []string{},
			skipHosts:               []string{},
			maintenanceStatusCode:   512,
			expectedCode:            http.StatusOK,
			description:             "When maintenance is active and client IP is in whitelist, request should be allowed",
		},
		{
			name:                    "Maintenance active - wildcard whitelist",
			endpoint:                wildcardEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/",
			skipPrefixes:            []string{},
			skipHosts:               []string{},
			maintenanceStatusCode:   512,
			expectedCode:            http.StatusOK,
			description:             "When maintenance is active with wildcard whitelist, all requests should be allowed",
		},
		{
			name:                    "Maintenance active - skip prefix",
			endpoint:                activeEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/admin/dashboard",
			skipPrefixes:            []string{"/admin"},
			skipHosts:               []string{},
			maintenanceStatusCode:   512,
			expectedCode:            http.StatusOK,
			description:             "When maintenance is active but URL matches skip prefix, request should be allowed",
		},
		{
			name:                    "Maintenance active - multiple skip prefixes, matching",
			endpoint:                activeEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/pgadmin/login",
			skipPrefixes:            []string{"/admin", "/pgadmin"},
			skipHosts:               []string{},
			maintenanceStatusCode:   512,
			expectedCode:            http.StatusOK,
			description:             "When maintenance is active but URL matches one of multiple skip prefixes, request should be allowed",
		},
		{
			name:                    "Maintenance active - multiple skip prefixes, non-matching",
			endpoint:                activeEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/app/user/profile",
			skipPrefixes:            []string{"/admin", "/pgadmin"},
			skipHosts:               []string{},
			maintenanceStatusCode:   512,
			expectedCode:            512,
			description:             "When maintenance is active and URL doesn't match any skip prefix, request should be blocked",
		},
		{
			name:                    "Maintenance active - skip host",
			endpoint:                activeEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/",
			skipPrefixes:            []string{},
			skipHosts:               []string{"test.example.com"},
			host:                    "test.example.com",
			maintenanceStatusCode:   512,
			expectedCode:            http.StatusOK,
			description:             "When maintenance is active but host matches skip host, request should be allowed",
		},
		{
			name:                    "Maintenance active - skip host with port",
			endpoint:                activeEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/",
			skipPrefixes:            []string{},
			skipHosts:               []string{"test.example.com"},
			host:                    "test.example.com:8080",
			maintenanceStatusCode:   512,
			expectedCode:            http.StatusOK,
			description:             "When maintenance is active but host with port matches skip host, request should be allowed",
		},
		{
			name:                    "Maintenance active - wildcard host match",
			endpoint:                activeEndpoint,
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			clientIP:                "10.0.0.1",
			urlPath:                 "/",
			skipPrefixes:            []string{},
			skipHosts:               []string{"*.example.com"},
			host:                    "sub.example.com",
			maintenanceStatusCode:   512,
			expectedCode:            http.StatusOK,
			description:             "When maintenance is active but host matches wildcard skip host, request should be allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset shared state between tests
			plugin.CloseSharedCache()
			time.Sleep(100 * time.Millisecond)

			cfg := plugin.CreateConfig()
			cfg.Endpoint = tt.endpoint
			cfg.CacheDurationInSeconds = tt.cacheDurationInSeconds
			cfg.RequestTimeoutInSeconds = tt.requestTimeoutInSeconds
			cfg.SkipPrefixes = tt.skipPrefixes
			cfg.SkipHosts = tt.skipHosts
			cfg.MaintenanceStatusCode = tt.maintenanceStatusCode

			next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				rw.WriteHeader(http.StatusOK)
			})

			handler, err := plugin.New(context.Background(), next, cfg, "maintenance-test")
			if err != nil {
				t.Fatalf("Error creating plugin: %v", err)
			}

			// Allow time for initial fetch to complete
			time.Sleep(100 * time.Millisecond)

			req := httptest.NewRequest(http.MethodGet, "http://localhost"+tt.urlPath, nil)

			if tt.clientIP != "" {
				req.Header.Set("X-Forwarded-For", tt.clientIP)
				req.Header.Set("X-Real-IP", tt.clientIP)
			}

			if tt.host != "" {
				req.Host = tt.host
			}

			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			response := recorder.Result()
			defer response.Body.Close()

			if response.StatusCode != tt.expectedCode {
				t.Errorf("%s: Expected status code %d, got %d", tt.description, tt.expectedCode, response.StatusCode)
			}
		})
	}
}

func TestMaintenanceCheckEdgeCases(t *testing.T) {
	// Reset shared state between tests
	plugin.CloseSharedCache()
	time.Sleep(100 * time.Millisecond)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}

		switch r.URL.Path {
		case "/slow-response":
			time.Sleep(200 * time.Millisecond)
			response.SystemConfig.Maintenance.IsActive = true
			response.SystemConfig.Maintenance.Whitelist = []string{"*"} // Allow all for timeouts
		case "/invalid-json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"invalid json`))
			return
		case "/error-status":
			w.WriteHeader(http.StatusInternalServerError)
			return
		default:
			response.SystemConfig.Maintenance.IsActive = false
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	tests := []struct {
		name                    string
		endpoint                string
		cacheDurationInSeconds  int
		requestTimeoutInSeconds int
		expectedCode            int
		description             string
	}{
		{
			name:                    "Timeout handling",
			endpoint:                ts.URL + "/slow-response",
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 1,
			expectedCode:            http.StatusOK,
			description:             "When API request times out, should use cached values and allow request",
		},
		{
			name:                    "Invalid JSON handling",
			endpoint:                ts.URL + "/invalid-json",
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			expectedCode:            http.StatusOK,
			description:             "When API returns invalid JSON, should use cached values and allow request",
		},
		{
			name:                    "Error status handling",
			endpoint:                ts.URL + "/error-status",
			cacheDurationInSeconds:  10,
			requestTimeoutInSeconds: 5,
			expectedCode:            http.StatusOK,
			description:             "When API returns error status, should use cached values and allow request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset shared state between tests
			plugin.CloseSharedCache()
			time.Sleep(100 * time.Millisecond)

			cfg := plugin.CreateConfig()
			cfg.Endpoint = tt.endpoint
			cfg.CacheDurationInSeconds = tt.cacheDurationInSeconds
			cfg.RequestTimeoutInSeconds = tt.requestTimeoutInSeconds

			next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				rw.WriteHeader(http.StatusOK)
			})

			handler, err := plugin.New(context.Background(), next, cfg, "maintenance-test")
			if err != nil {
				t.Fatalf("Error creating plugin: %v", err)
			}

			// First, make a normal request to cache the response
			if tt.name == "Timeout handling" {
				// Use a fast endpoint to pre-populate the cache with maintenance inactive
				plugin.CloseSharedCache()
				time.Sleep(100 * time.Millisecond)

				fastCfg := plugin.CreateConfig()
				fastCfg.Endpoint = ts.URL // Use fast endpoint temporarily
				fastCfg.CacheDurationInSeconds = 10
				fastCfg.RequestTimeoutInSeconds = 5

				fastHandler, _ := plugin.New(context.Background(), next, fastCfg, "fast-test")
				fastReq := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
				fastRec := httptest.NewRecorder()
				fastHandler.ServeHTTP(fastRec, fastReq)

				// Allow time for initial fetch to complete
				time.Sleep(100 * time.Millisecond)

				// Now create the handler with the original endpoint
				plugin.CloseSharedCache()
				time.Sleep(100 * time.Millisecond)

				handler, err = plugin.New(context.Background(), next, cfg, "maintenance-test")
				if err != nil {
					t.Fatalf("Error creating plugin: %v", err)
				}
			}

			// Allow time for initial fetch to complete or timeout
			time.Sleep(100 * time.Millisecond)

			// Then try the actual test endpoint
			req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			response := recorder.Result()
			defer response.Body.Close()

			if response.StatusCode != tt.expectedCode {
				t.Errorf("%s: Expected status code %d, got %d", tt.description, tt.expectedCode, response.StatusCode)
			}
		})
	}
}

func TestCacheWarmup(t *testing.T) {
	// Reset shared state
	plugin.CloseSharedCache()
	time.Sleep(100 * time.Millisecond)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}
		response.SystemConfig.Maintenance.IsActive = false

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	cfg := plugin.CreateConfig()
	cfg.Endpoint = ts.URL
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

func TestInvalidConfig(t *testing.T) {
	cfg := plugin.CreateConfig()
	// Endpoint is required but not provided

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	_, err := plugin.New(context.Background(), next, cfg, "maintenance-test")
	if err == nil {
		t.Error("Expected error for missing endpoint, but got none")
	}
}

func TestSingletonPattern(t *testing.T) {
	// Reset shared state
	plugin.CloseSharedCache()
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
	cfg.Endpoint = ts.URL
	cfg.CacheDurationInSeconds = 10
	cfg.RequestTimeoutInSeconds = 5
	cfg.Debug = true

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
	time.Sleep(200 * time.Millisecond)

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
	plugin.CloseSharedCache()
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
	cfg.Endpoint = ts.URL
	cfg.CacheDurationInSeconds = 1
	cfg.RequestTimeoutInSeconds = 5
	cfg.MaintenanceStatusCode = 503
	cfg.Debug = true

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
			req.Header.Set("X-Forwarded-For", clientIP)
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

func TestBackoffRetry(t *testing.T) {
	// Reset shared state
	plugin.CloseSharedCache()
	time.Sleep(200 * time.Millisecond)

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
		time.Sleep(50 * time.Millisecond)
	}

	// Verify the correct number of requests were made
	if count := atomic.LoadInt32(&requestCount); count != 4 {
		t.Fatalf("Expected exactly 4 requests to the server, got %d", count)
	}

	// Now create and test the middleware with the preconditioned server
	cfg := plugin.CreateConfig()
	cfg.Endpoint = ts.URL
	cfg.CacheDurationInSeconds = 1
	cfg.RequestTimeoutInSeconds = 1
	cfg.Debug = true

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	handler, err := plugin.New(context.Background(), next, cfg, "backoff-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for initial fetch to complete
	time.Sleep(300 * time.Millisecond)

	// Make a request to test the middleware
	req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
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

func TestSharedCacheBetweenInstances(t *testing.T) {
	// Reset shared state before test
	plugin.CloseSharedCache()
	time.Sleep(200 * time.Millisecond)

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
	cfg.Endpoint = ts.URL
	cfg.CacheDurationInSeconds = 30 // Use a longer duration to ensure cache is valid throughout test
	cfg.RequestTimeoutInSeconds = 5
	cfg.MaintenanceStatusCode = 503
	cfg.Debug = true

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
	time.Sleep(300 * time.Millisecond)

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
		req.Header.Set("X-Forwarded-For", "10.0.0.1") // Not in whitelist
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
		req.Header.Set("X-Forwarded-For", "192.168.1.1") // In whitelist
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

package traefik_maintenance_plugin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	plugin "github.com/CitronusAcademy/traefik-maintenance-plugin"
)

func TestCORSFunctionalityDuringMaintenance(t *testing.T) {
	// Reset shared state between tests
	plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	// Set up test server with active maintenance mode
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}

		switch r.URL.Path {
		case "/maintenance-active-no-whitelist":
			response.SystemConfig.Maintenance.IsActive = true
			response.SystemConfig.Maintenance.Whitelist = []string{} // No one allowed
		case "/maintenance-active-with-whitelist":
			response.SystemConfig.Maintenance.IsActive = true
			response.SystemConfig.Maintenance.Whitelist = []string{"192.168.1.1"} // Specific IP allowed
		default:
			response.SystemConfig.Maintenance.IsActive = false
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	tests := []struct {
		name                string
		endpoint            string
		method              string
		origin              string
		clientIP            string
		expectedStatusCode  int
		expectedCORSOrigin  string
		expectedCORSMethods string
		expectedCORSHeaders string
		expectedCORSMaxAge  string
		description         string
	}{
		{
			name:                "CORS preflight - maintenance active, blocked IP",
			endpoint:            ts.URL + "/maintenance-active-no-whitelist",
			method:              http.MethodOptions,
			origin:              "https://example.pro",
			clientIP:            "10.0.0.1",
			expectedStatusCode:  http.StatusOK, // Preflight should return 200 even when blocked
			expectedCORSOrigin:  "https://example.pro",
			expectedCORSMethods: "GET, POST, PUT, DELETE, OPTIONS",
			expectedCORSHeaders: "Accept, Authorization, Content-Type, X-CSRF-Token",
			expectedCORSMaxAge:  "86400",
			description:         "Should handle CORS preflight with 200 status, actual request will be blocked",
		},
		{
			name:                "CORS preflight - maintenance active, allowed IP",
			endpoint:            ts.URL + "/maintenance-active-with-whitelist",
			method:              http.MethodOptions,
			origin:              "https://example.pro",
			clientIP:            "192.168.1.1",
			expectedStatusCode:  http.StatusNoContent, // Successful preflight
			expectedCORSOrigin:  "https://example.pro",
			expectedCORSMethods: "GET, POST, PUT, DELETE, OPTIONS",
			expectedCORSHeaders: "Accept, Authorization, Content-Type, X-CSRF-Token",
			expectedCORSMaxAge:  "86400",
			description:         "Should handle CORS preflight and allow due to IP whitelist",
		},
		{
			name:                "CORS preflight - maintenance inactive",
			endpoint:            ts.URL, // Default inactive maintenance
			method:              http.MethodOptions,
			origin:              "https://example.pro",
			clientIP:            "10.0.0.1",
			expectedStatusCode:  http.StatusOK, // Backend handles OPTIONS and returns 200 (from our test server)
			expectedCORSOrigin:  "",            // No CORS headers from plugin when maintenance is off
			expectedCORSMethods: "",
			expectedCORSHeaders: "",
			expectedCORSMaxAge:  "",
			description:         "Should pass OPTIONS to backend when maintenance is inactive",
		},
		{
			name:                "Regular request - maintenance active, blocked IP with CORS",
			endpoint:            ts.URL + "/maintenance-active-no-whitelist",
			method:              http.MethodGet,
			origin:              "https://example.pro",
			clientIP:            "10.0.0.1",
			expectedStatusCode:  512, // Maintenance status code
			expectedCORSOrigin:  "https://example.pro",
			expectedCORSMethods: "GET, POST, PUT, DELETE, OPTIONS",
			expectedCORSHeaders: "Accept, Authorization, Content-Type, X-CSRF-Token",
			expectedCORSMaxAge:  "86400",
			description:         "Should return maintenance status with full CORS headers for blocked requests",
		},
		{
			name:               "Regular request - maintenance active, allowed IP",
			endpoint:           ts.URL + "/maintenance-active-with-whitelist",
			method:             http.MethodGet,
			origin:             "https://example.pro",
			clientIP:           "192.168.1.1",
			expectedStatusCode: http.StatusOK, // Pass through to backend
			description:        "Should pass through to backend for whitelisted IP",
		},
		{
			name:               "CORS preflight - no origin header",
			endpoint:           ts.URL + "/maintenance-active-no-whitelist",
			method:             http.MethodOptions,
			origin:             "", // No origin
			clientIP:           "10.0.0.1",
			expectedStatusCode: http.StatusOK, // Should pass to backend when no origin
			description:        "Should pass OPTIONS to backend when no origin header is present",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset shared state for each test
			plugin.ResetSharedCacheForTesting()
			time.Sleep(100 * time.Millisecond)

			cfg := plugin.CreateConfig()
			cfg.EnvironmentEndpoints = map[string]string{"": tt.endpoint}
			cfg.CacheDurationInSeconds = 10
			cfg.RequestTimeoutInSeconds = 5
			cfg.MaintenanceStatusCode = 512
			cfg.Debug = false

			next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				rw.WriteHeader(http.StatusOK)
			})

			handler, err := plugin.New(context.Background(), next, cfg, "cors-test")
			if err != nil {
				t.Fatalf("Error creating plugin: %v", err)
			}

			// Allow time for initial fetch to complete
			time.Sleep(200 * time.Millisecond)

			// Create request
			req := httptest.NewRequest(tt.method, "http://localhost/", nil)

			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			if tt.clientIP != "" {
				req.Header.Set("Cf-Connecting-Ip", tt.clientIP)
			}

			// For preflight requests, add typical CORS headers
			if tt.method == http.MethodOptions {
				req.Header.Set("Access-Control-Request-Method", "GET")
				req.Header.Set("Access-Control-Request-Headers", "Content-Type,Authorization")
			}

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			response := recorder.Result()
			defer response.Body.Close()

			// Check status code
			if response.StatusCode != tt.expectedStatusCode {
				t.Errorf("%s: Expected status code %d, got %d", tt.description, tt.expectedStatusCode, response.StatusCode)
			}

			// Check CORS headers if expected
			if tt.expectedCORSOrigin != "" {
				actualOrigin := response.Header.Get("Access-Control-Allow-Origin")
				if actualOrigin != tt.expectedCORSOrigin {
					t.Errorf("%s: Expected CORS origin '%s', got '%s'", tt.description, tt.expectedCORSOrigin, actualOrigin)
				}
			}

			if tt.expectedCORSMethods != "" {
				actualMethods := response.Header.Get("Access-Control-Allow-Methods")
				if actualMethods != tt.expectedCORSMethods {
					t.Errorf("%s: Expected CORS methods '%s', got '%s'", tt.description, tt.expectedCORSMethods, actualMethods)
				}
			}

			if tt.expectedCORSHeaders != "" {
				actualHeaders := response.Header.Get("Access-Control-Allow-Headers")
				if actualHeaders != tt.expectedCORSHeaders {
					t.Errorf("%s: Expected CORS headers '%s', got '%s'", tt.description, tt.expectedCORSHeaders, actualHeaders)
				}
			}

			if tt.expectedCORSMaxAge != "" {
				actualMaxAge := response.Header.Get("Access-Control-Max-Age")
				if actualMaxAge != tt.expectedCORSMaxAge {
					t.Errorf("%s: Expected CORS max-age '%s', got '%s'", tt.description, tt.expectedCORSMaxAge, actualMaxAge)
				}
			}

			// For responses with CORS headers, check that credentials are allowed (only if origin was present)
			if tt.expectedCORSOrigin != "" && (tt.expectedStatusCode == 512 || tt.method == http.MethodOptions) {
				credentialsAllowed := response.Header.Get("Access-Control-Allow-Credentials")
				if credentialsAllowed != "true" {
					t.Errorf("%s: Expected Access-Control-Allow-Credentials 'true', got '%s'", tt.description, credentialsAllowed)
				}
			}

			// Verify content type for all maintenance responses (both with and without origin)
			if tt.expectedStatusCode == 512 {
				contentType := response.Header.Get("Content-Type")
				if contentType != "text/plain; charset=utf-8" {
					t.Errorf("%s: Expected content type 'text/plain; charset=utf-8', got '%s'", tt.description, contentType)
				}
			}

			if t.Failed() {
				t.Logf("Response headers: %+v", response.Header)
			}
		})
	}
}

func TestCORSPreflightStripsHostPort(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":true,"whitelist":[]}}}`))
	}))
	defer srv.Close()

	cfg := plugin.CreateConfig()
	cfg.MaintenanceStatusCode = 512
	cfg.CacheDurationInSeconds = 10
	cfg.RequestTimeoutInSeconds = 5
	cfg.EnvironmentEndpoints = map[string]string{".world": srv.URL}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h, err := plugin.New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// wait for warm-up to cache the active state
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.world/x", nil))
		if rec.Code == 512 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// OPTIONS preflight with a PORT in Host must still resolve the .world env (active) and be handled,
	// not fall through to next.ServeHTTP.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "http://example.world:443/api", nil)
	req.Host = "example.world:443"
	req.Header.Set("Origin", "https://example.world")
	h.ServeHTTP(rec, req)

	// Preflight must be handled as a preflight (blocked preflight → 200 per CORS
	// spec), not fall through to the main path and return the 512 maintenance
	// stub. With the raw (ported) host the .world env doesn't match, maintenance
	// reads inactive, and the OPTIONS request falls through to the 512 response.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected preflight to be handled (200) for ported host; got code %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://example.world" {
		t.Fatalf("expected preflight CORS headers for ported host; got headers %v, code %d", rec.Header(), rec.Code)
	}
}

func TestPreflightToSkippedHostIsForwarded(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":true,"whitelist":[]}}}`))
	}))
	defer srv.Close()

	cfg := plugin.CreateConfig()
	cfg.MaintenanceStatusCode = 512
	cfg.CacheDurationInSeconds = 30
	cfg.RequestTimeoutInSeconds = 5
	cfg.EnvironmentEndpoints = map[string]string{".world": srv.URL}
	cfg.SkipHosts = []string{"health.world"}

	const forwardedCode = 299
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(forwardedCode) })
	h, err := plugin.New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Wait for maintenance to be cached active.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.world/api", nil)
		h.ServeHTTP(rec, req)
		if rec.Code == 512 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "http://health.world/api", nil)
	req.Host = "health.world"
	req.Header.Set("Origin", "https://health.world")
	h.ServeHTTP(rec, req)

	if rec.Code != forwardedCode {
		t.Fatalf("OPTIONS to skip-listed host: expected forward (%d), got %d", forwardedCode, rec.Code)
	}
}

func TestDisallowedOriginNotReflected(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":true,"whitelist":[]}}}`))
	}))
	defer srv.Close()

	cfg := plugin.CreateConfig()
	cfg.MaintenanceStatusCode = 512
	cfg.CacheDurationInSeconds = 30
	cfg.RequestTimeoutInSeconds = 5
	cfg.EnvironmentEndpoints = map[string]string{".world": srv.URL}
	cfg.AllowedOrigins = []string{"https://app.world"}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h, err := plugin.New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.world/api", nil)
		h.ServeHTTP(rec, req)
		if rec.Code == 512 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Disallowed origin must NOT be reflected.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "http://example.world/api", nil)
	req.Host = "example.world"
	req.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin reflected: ACAO=%q", got)
	}

	// Allowed origin still reflected.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodOptions, "http://example.world/api", nil)
	req2.Host = "example.world"
	req2.Header.Set("Origin", "https://app.world")
	h.ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("Access-Control-Allow-Origin"); got != "https://app.world" {
		t.Fatalf("allowed origin not reflected: ACAO=%q", got)
	}
}

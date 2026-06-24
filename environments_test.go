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

func TestKubernetesEnvironment(t *testing.T) {
	// Reset shared state between tests
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	// Set up test server with active maintenance mode
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}
		response.SystemConfig.Maintenance.IsActive = true
		// Whitelist for external IPs, not internal k8s IPs
		response.SystemConfig.Maintenance.Whitelist = []string{"203.0.113.0/24"}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL}
	cfg.CacheDurationInSeconds = 10
	cfg.RequestTimeoutInSeconds = 5
	cfg.MaintenanceStatusCode = 503
	cfg.Debug = false

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	handler, err := plugin.New(context.Background(), next, cfg, "k8s-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for initial fetch to complete
	time.Sleep(200 * time.Millisecond)

	tests := []struct {
		name           string
		headers        map[string]string
		remoteAddr     string
		expectedStatus int
		description    string
	}{
		{
			name: "External traffic through Cloudflare - allowed",
			headers: map[string]string{
				// Cloudflare provides this header with true client IP
				"Cf-Connecting-Ip": "203.0.113.42",
				"User-Agent":       "Mozilla/5.0",
			},
			remoteAddr:     "10.10.122.113:12345",
			expectedStatus: http.StatusOK,
			description:    "Should allow external traffic from whitelisted IP via Cf-Connecting-Ip",
		},
		{
			name: "External traffic through Cloudflare - blocked",
			headers: map[string]string{
				// Cloudflare provides this header with true client IP
				"Cf-Connecting-Ip": "192.0.2.42",
				"User-Agent":       "Mozilla/5.0",
			},
			remoteAddr:     "10.10.122.113:12345",
			expectedStatus: 503,
			description:    "Should block external traffic from non-whitelisted IP via Cf-Connecting-Ip",
		},
		{
			name: "Internal k8s traffic without Cf-Connecting-Ip",
			headers: map[string]string{
				// No Cf-Connecting-Ip header
				"User-Agent": "Go-http-client/1.1",
			},
			remoteAddr:     "10.10.122.113:12345",
			expectedStatus: 503, // Will be blocked since no Cf-Connecting-Ip header
			description:    "Internal k8s traffic without Cf-Connecting-Ip should be blocked",
		},
		{
			name: "Cloudflare with CSV IPs - one allowed",
			headers: map[string]string{
				// Cloudflare provides CSV with multiple IPs
				"Cf-Connecting-Ip": "192.0.2.45, 203.0.113.99",
				"User-Agent":       "Mozilla/5.0",
			},
			remoteAddr:     "10.10.122.113:12345",
			expectedStatus: http.StatusOK, // Should allow because one IP in CSV is whitelisted
			description:    "Cloudflare CSV should allow if any IP matches whitelist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)

			// Set all headers
			for name, value := range tt.headers {
				req.Header.Set(name, value)
			}

			// Set remote address
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			response := recorder.Result()
			defer response.Body.Close()

			if response.StatusCode != tt.expectedStatus {
				t.Errorf("%s: Expected status code %d, got %d", tt.description, tt.expectedStatus, response.StatusCode)
			}
		})
	}
}

func TestProductionComDomainSupport(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}

		switch r.URL.Path {
		case "/prod-maintenance-active":
			response.SystemConfig.Maintenance.IsActive = true
			response.SystemConfig.Maintenance.Whitelist = []string{"203.0.113.0/24"} // Production IP range
		case "/prod-maintenance-inactive":
			response.SystemConfig.Maintenance.IsActive = false
		default:
			response.SystemConfig.Maintenance.IsActive = false
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	tests := []struct {
		name            string
		customEndpoints map[string]string
		testDomain      string
		clientIP        string
		expectedStatus  int
		description     string
	}{
		{
			name: "Production .com domain with custom endpoint",
			customEndpoints: map[string]string{
				".com": ts.URL + "/prod-maintenance-active",
				"":     ts.URL + "/prod-maintenance-inactive", // default fallback
			},
			testDomain:     "api.example.com",
			clientIP:       "203.0.113.100", // In whitelist range
			expectedStatus: http.StatusOK,
			description:    "Should route .com domain to custom production endpoint and allow whitelisted IP",
		},
		{
			name: "Production .com domain - blocked IP",
			customEndpoints: map[string]string{
				".com": ts.URL + "/prod-maintenance-active",
				"":     ts.URL + "/prod-maintenance-inactive",
			},
			testDomain:     "portal.example.com",
			clientIP:       "10.0.0.1", // Not in whitelist
			expectedStatus: 512,
			description:    "Should route .com domain to custom production endpoint and block non-whitelisted IP",
		},
		{
			name: "Non-.com domain uses default endpoint",
			customEndpoints: map[string]string{
				".com": ts.URL + "/prod-maintenance-active",
				"":     ts.URL + "/prod-maintenance-inactive",
			},
			testDomain:     "test.example.org",
			clientIP:       "10.0.0.1",
			expectedStatus: http.StatusOK,
			description:    "Should use default endpoint for domains that don't match .com",
		},
		{
			name: "Multiple custom domains",
			customEndpoints: map[string]string{
				".com":   ts.URL + "/prod-maintenance-active",
				".local": ts.URL + "/prod-maintenance-inactive",
				".dev":   ts.URL + "/prod-maintenance-inactive",
				"":       ts.URL + "/prod-maintenance-inactive",
			},
			testDomain:     "staging.example.local",
			clientIP:       "192.168.1.1",
			expectedStatus: http.StatusOK,
			description:    "Should support multiple custom domain endpoints",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin.ResetSharedCacheForTesting()
			time.Sleep(100 * time.Millisecond)

			cfg := plugin.CreateConfig()
			cfg.EnvironmentEndpoints = tt.customEndpoints
			cfg.CacheDurationInSeconds = 10
			cfg.RequestTimeoutInSeconds = 5
			cfg.MaintenanceStatusCode = 512
			cfg.Debug = false

			next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				rw.WriteHeader(http.StatusOK)
			})

			handler, err := plugin.New(context.Background(), next, cfg, "prod-com-test")
			if err != nil {
				t.Fatalf("Error creating plugin: %v", err)
			}

			time.Sleep(200 * time.Millisecond)

			req := httptest.NewRequest(http.MethodGet, "http://"+tt.testDomain+"/", nil)
			req.Host = tt.testDomain

			if tt.clientIP != "" {
				req.Header.Set("Cf-Connecting-Ip", tt.clientIP)
			}

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			response := recorder.Result()
			defer response.Body.Close()

			if response.StatusCode != tt.expectedStatus {
				t.Errorf("%s: Expected status code %d, got %d", tt.description, tt.expectedStatus, response.StatusCode)
			}
		})
	}
}

func TestConfigurationOverridesBehavior(t *testing.T) {
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}
		response.SystemConfig.Maintenance.IsActive = false
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{
		".production": ts.URL,
		".staging":    ts.URL,
		"":            ts.URL,
	}

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	handler, err := plugin.New(context.Background(), next, cfg, "config-override-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	tests := []struct {
		domain         string
		expectedStatus int
		description    string
	}{
		{
			domain:         "app.example.production",
			expectedStatus: http.StatusOK,
			description:    "Custom .production domain should work",
		},
		{
			domain:         "test.example.staging",
			expectedStatus: http.StatusOK,
			description:    "Custom .staging domain should work",
		},
		{
			domain:         "legacy.example.com",
			expectedStatus: http.StatusOK,
			description:    "Non-matching domain should use default endpoint",
		},
		{
			domain:         "dev.example.world",
			expectedStatus: http.StatusOK,
			description:    "Previously hardcoded .world should now use default endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.domain+"/", nil)
			req.Host = tt.domain

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			response := recorder.Result()
			defer response.Body.Close()

			if response.StatusCode != tt.expectedStatus {
				t.Errorf("%s: Expected status code %d, got %d", tt.description, tt.expectedStatus, response.StatusCode)
			}
		})
	}
}

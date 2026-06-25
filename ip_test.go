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

func TestIPDetectionFromCfConnectingIp(t *testing.T) {
	// Reset shared state between tests
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	ts, regularEndpoint, _, _, _ := setupTestServer()
	defer ts.Close()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": regularEndpoint}
	cfg.Debug = false

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	handler, err := plugin.New(context.Background(), next, cfg, "ip-detection-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for initial fetch to complete
	time.Sleep(200 * time.Millisecond)

	tests := []struct {
		name           string
		headers        map[string]string
		expectedStatus int
		description    string
	}{
		{
			name: "Cf-Connecting-Ip header - single IP",
			headers: map[string]string{
				"Cf-Connecting-Ip": "203.0.113.3",
			},
			expectedStatus: http.StatusOK,
			description:    "Should detect IP from Cf-Connecting-Ip header (CloudFlare)",
		},
		{
			name: "Cf-Connecting-Ip with multiple IPs (CSV)",
			headers: map[string]string{
				"Cf-Connecting-Ip": "203.0.113.11, 10.0.0.1, 172.16.0.1",
			},
			expectedStatus: http.StatusOK,
			description:    "Should handle CSV format in Cf-Connecting-Ip",
		},
		{
			name: "IPv6 address in Cf-Connecting-Ip",
			headers: map[string]string{
				"Cf-Connecting-Ip": "2001:db8::1",
			},
			expectedStatus: http.StatusOK,
			description:    "Should handle IPv6 addresses correctly",
		},
		{
			name: "IPv6 address with port in Cf-Connecting-Ip",
			headers: map[string]string{
				"Cf-Connecting-Ip": "[2001:db8::1]:8080",
			},
			expectedStatus: http.StatusOK,
			description:    "Should handle IPv6 addresses with port correctly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)

			// Set headers
			for name, value := range tt.headers {
				req.Header.Set(name, value)
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

func TestCIDRWhitelist(t *testing.T) {
	// Reset shared state between tests
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	// Create custom test server with CIDR whitelist
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}

		switch r.URL.Path {
		case "/maintenance-active-cidr":
			response.SystemConfig.Maintenance.IsActive = true
			// Whitelist 192.168.0.0/16 network and a specific IP
			response.SystemConfig.Maintenance.Whitelist = []string{"192.168.0.0/16", "10.0.0.1"}
		default:
			response.SystemConfig.Maintenance.IsActive = false
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	cidrEndpoint := ts.URL + "/maintenance-active-cidr"

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": cidrEndpoint}
	cfg.CacheDurationInSeconds = 10
	cfg.RequestTimeoutInSeconds = 5
	cfg.MaintenanceStatusCode = 503
	cfg.Debug = false

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	handler, err := plugin.New(context.Background(), next, cfg, "cidr-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for initial fetch to complete
	time.Sleep(200 * time.Millisecond)

	tests := []struct {
		name           string
		clientIP       string
		expectedStatus int
		description    string
	}{
		{
			name:           "IP in exact whitelist",
			clientIP:       "10.0.0.1",
			expectedStatus: http.StatusOK,
			description:    "Should allow IP that exactly matches a whitelist entry",
		},
		{
			name:           "IP in CIDR range",
			clientIP:       "192.168.1.1",
			expectedStatus: http.StatusOK,
			description:    "Should allow IP that is within CIDR range in whitelist",
		},
		{
			name:           "IP in CIDR range (edge case)",
			clientIP:       "192.168.255.255",
			expectedStatus: http.StatusOK,
			description:    "Should allow IP at the edge of CIDR range in whitelist",
		},
		{
			name:           "IP outside CIDR range",
			clientIP:       "172.16.0.1",
			expectedStatus: 503,
			description:    "Should block IP outside of CIDR range and not in whitelist",
		},
		{
			name:           "IP outside CIDR but numerically close",
			clientIP:       "193.168.0.1",
			expectedStatus: 503,
			description:    "Should block IP just outside of CIDR range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
			req.Header.Set("Cf-Connecting-Ip", tt.clientIP)

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

func TestIPv6WhitelistCanonicalization(t *testing.T) {
	// Reset shared state between tests
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	// Whitelist entries use non-canonical IPv6 forms (mixed case, expanded);
	// the plugin must still match canonical/compressed client IPs.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}
		response.SystemConfig.Maintenance.IsActive = true
		response.SystemConfig.Maintenance.Whitelist = []string{
			"2001:DB8::1", // upper-case
			"2001:0db8:0000:0000:0000:0000:0000:0002", // fully expanded
			"office", // non-IP entry, must stay exact-string
		}

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

	handler, err := plugin.New(context.Background(), next, cfg, "ipv6-canon-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for initial fetch to complete
	time.Sleep(200 * time.Millisecond)

	tests := []struct {
		name           string
		clientIP       string
		expectedStatus int
		description    string
	}{
		{
			name:           "upper-case entry matches canonical client",
			clientIP:       "2001:db8::1",
			expectedStatus: http.StatusOK,
			description:    "2001:DB8::1 whitelist entry should match 2001:db8::1 client",
		},
		{
			name:           "expanded entry matches compressed client",
			clientIP:       "2001:db8::2",
			expectedStatus: http.StatusOK,
			description:    "fully-expanded whitelist entry should match compressed client",
		},
		{
			name:           "non-ip entry stays exact-string match",
			clientIP:       "office",
			expectedStatus: http.StatusOK,
			description:    "non-IP whitelist entry should still match by raw string",
		},
		{
			name:           "unrelated ipv6 client blocked",
			clientIP:       "2001:db8::ffff",
			expectedStatus: 503,
			description:    "an IPv6 client not in the whitelist should be blocked",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
			req.Header.Set("Cf-Connecting-Ip", tt.clientIP)

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

func TestInvalidIPAndCIDRHandling(t *testing.T) {
	// Reset shared state between tests
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	// Create custom test server with invalid whitelist entries
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}

		switch r.URL.Path {
		case "/maintenance-active-invalid-cidr":
			response.SystemConfig.Maintenance.IsActive = true
			// Include some invalid CIDR notations and IPs
			response.SystemConfig.Maintenance.Whitelist = []string{
				"192.168.0.0/16", // Valid CIDR
				"10.0.0.256",     // Invalid IP (256 is out of range)
				"172.16.0.0/33",  // Invalid CIDR (prefix length > 32)
				"not-an-ip",      // Completely invalid
				"203.0.113.1",    // Valid IP
			}
		default:
			response.SystemConfig.Maintenance.IsActive = false
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	invalidEndpoint := ts.URL + "/maintenance-active-invalid-cidr"

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": invalidEndpoint}
	cfg.CacheDurationInSeconds = 10
	cfg.RequestTimeoutInSeconds = 5
	cfg.MaintenanceStatusCode = 503
	cfg.Debug = false

	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	handler, err := plugin.New(context.Background(), next, cfg, "invalid-ip-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for initial fetch to complete
	time.Sleep(200 * time.Millisecond)

	tests := []struct {
		name           string
		clientIP       string
		expectedStatus int
		description    string
	}{
		{
			name:           "Valid IP in valid CIDR range",
			clientIP:       "192.168.1.1",
			expectedStatus: http.StatusOK,
			description:    "Should allow IP in valid CIDR range",
		},
		{
			name:           "Valid IP matching valid whitelist entry",
			clientIP:       "203.0.113.1",
			expectedStatus: http.StatusOK,
			description:    "Should allow IP that exactly matches a valid whitelist entry",
		},
		{
			name:           "Non-matching IP",
			clientIP:       "8.8.8.8",
			expectedStatus: 503,
			description:    "Should block IP that doesn't match any valid whitelist entry",
		},
		{
			name:           "Invalid IP format in request",
			clientIP:       "invalid-request-ip",
			expectedStatus: 503,
			description:    "Should handle invalid IP in request gracefully and block",
		},
		{
			name:           "Empty IP",
			clientIP:       "",
			expectedStatus: 503,
			description:    "Should handle empty IP gracefully and block",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)

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

func TestCfConnectingIpHeader(t *testing.T) {
	// Reset shared state between tests
	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()
	time.Sleep(100 * time.Millisecond)

	// Create test server with specific IP whitelist
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := maintenanceResponse{}
		response.SystemConfig.Maintenance.IsActive = true
		// Only allow specific IPs
		response.SystemConfig.Maintenance.Whitelist = []string{"192.168.1.100", "10.0.0.50", "203.0.113.0/24"}

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

	handler, err := plugin.New(context.Background(), next, cfg, "cf-ip-test")
	if err != nil {
		t.Fatalf("Error creating plugin: %v", err)
	}

	// Allow time for initial fetch to complete
	time.Sleep(200 * time.Millisecond)

	tests := []struct {
		name           string
		headers        map[string]string
		expectedStatus int
		description    string
	}{
		{
			name: "Cf-Connecting-Ip single value - allowed",
			headers: map[string]string{
				"Cf-Connecting-Ip": "192.168.1.100",
			},
			expectedStatus: http.StatusOK,
			description:    "Should allow when single Cf-Connecting-Ip is whitelisted",
		},
		{
			name: "Cf-Connecting-Ip single value - blocked",
			headers: map[string]string{
				"Cf-Connecting-Ip": "8.8.8.8",
			},
			expectedStatus: 503,
			description:    "Should block when single Cf-Connecting-Ip is not whitelisted",
		},
		{
			name: "Cf-Connecting-Ip CSV - first allowed",
			headers: map[string]string{
				"Cf-Connecting-Ip": "192.168.1.100, 10.10.10.10, 172.16.0.1",
			},
			expectedStatus: http.StatusOK,
			description:    "Should allow when first IP in CSV is whitelisted",
		},
		{
			name: "Cf-Connecting-Ip CSV - middle allowed",
			headers: map[string]string{
				"Cf-Connecting-Ip": "8.8.8.8, 10.0.0.50, 172.16.0.1",
			},
			expectedStatus: http.StatusOK,
			description:    "Should allow when any IP in CSV is whitelisted",
		},
		{
			name: "Cf-Connecting-Ip CSV - last allowed",
			headers: map[string]string{
				"Cf-Connecting-Ip": "8.8.8.8, 1.1.1.1, 192.168.1.100",
			},
			expectedStatus: http.StatusOK,
			description:    "Should allow when last IP in CSV is whitelisted",
		},
		{
			name: "Cf-Connecting-Ip CSV - none allowed",
			headers: map[string]string{
				"Cf-Connecting-Ip": "8.8.8.8, 1.1.1.1, 172.16.0.1",
			},
			expectedStatus: 503,
			description:    "Should block when no IP in CSV is whitelisted",
		},
		{
			name: "Cf-Connecting-Ip CSV - CIDR match",
			headers: map[string]string{
				"Cf-Connecting-Ip": "8.8.8.8, 203.0.113.42, 1.1.1.1",
			},
			expectedStatus: http.StatusOK,
			description:    "Should allow when any IP in CSV matches CIDR whitelist",
		},
		{
			name: "Cf-Connecting-Ip CSV with spaces",
			headers: map[string]string{
				"Cf-Connecting-Ip": "  8.8.8.8  ,  192.168.1.100  ,  1.1.1.1  ",
			},
			expectedStatus: http.StatusOK,
			description:    "Should handle spaces around IPs in CSV",
		},
		{
			name: "Cf-Connecting-Ip with port",
			headers: map[string]string{
				"Cf-Connecting-Ip": "192.168.1.100:8080",
			},
			expectedStatus: http.StatusOK,
			description:    "Should strip port from IP and allow whitelisted IP",
		},
		{
			name: "Only X-Forwarded-For present - should be ignored",
			headers: map[string]string{
				"X-Forwarded-For": "192.168.1.100",
			},
			expectedStatus: 503,
			description:    "Should ignore X-Forwarded-For and block when Cf-Connecting-Ip is missing",
		},
		{
			name: "Both headers present - only Cf-Connecting-Ip used",
			headers: map[string]string{
				"Cf-Connecting-Ip": "8.8.8.8",
				"X-Forwarded-For":  "192.168.1.100",
			},
			expectedStatus: 503,
			description:    "Should only use Cf-Connecting-Ip even if X-Forwarded-For has whitelisted IP",
		},
		{
			name:           "No IP headers present",
			headers:        map[string]string{},
			expectedStatus: 503,
			description:    "Should block when no Cf-Connecting-Ip header is present",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)

			// Set headers
			for name, value := range tt.headers {
				req.Header.Set(name, value)
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

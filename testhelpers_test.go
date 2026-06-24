package traefik_maintenance_plugin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"
)

type maintenanceResponse struct {
	SystemConfig struct {
		Maintenance struct {
			IsActive  bool     `json:"is_active"`
			Whitelist []string `json:"whitelist,omitempty"`
		} `json:"maintenance"`
	} `json:"system_config"`
}

func nextNoop() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})
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

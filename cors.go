package traefik_maintenance_plugin

import (
	"fmt"
	"net/http"
	"os"
)

func (m *MaintenanceCheck) handleCORSPreflightRequest(rw http.ResponseWriter, req *http.Request) bool {
	if req.Method != http.MethodOptions {
		return false
	}

	isActive, whitelist := getMaintenanceStatusForDomain(m.extractHostWithoutPort(req.Host))
	if !isActive {
		return false
	}

	clientOrigin := req.Header.Get("Origin")
	m.setCORSPreflightHeaders(rw, clientOrigin)

	if !m.isClientAllowed(req, whitelist) {
		m.sendBlockedPreflightResponse(rw)
		return true
	}

	m.sendSuccessfulPreflightResponse(rw)
	return true
}

func (m *MaintenanceCheck) setCORSPreflightHeaders(rw http.ResponseWriter, origin string) {
	if origin == "" {
		return
	}

	corsHeaders := map[string]string{
		"Access-Control-Allow-Origin":      origin,
		"Access-Control-Allow-Methods":     "GET, POST, PUT, DELETE, OPTIONS",
		"Access-Control-Allow-Headers":     "Accept, Authorization, Content-Type, X-CSRF-Token",
		"Access-Control-Allow-Credentials": "true",
		"Access-Control-Max-Age":           "86400",
	}

	for headerName, headerValue := range corsHeaders {
		rw.Header().Set(headerName, headerValue)
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] CORS preflight handled for origin: %s\n", origin)
	}
}

func (m *MaintenanceCheck) sendBlockedPreflightResponse(rw http.ResponseWriter) {
	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] CORS preflight completed, but actual request will be blocked due to maintenance mode\n")
	}

	// Preflight must always return 2xx status according to CORS spec
	rw.WriteHeader(http.StatusOK)
}

func (m *MaintenanceCheck) sendSuccessfulPreflightResponse(rw http.ResponseWriter) {
	rw.WriteHeader(http.StatusNoContent)
}

func (m *MaintenanceCheck) sendMaintenanceResponseWithCORS(rw http.ResponseWriter, req *http.Request) {
	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Access denied, returning status code %d\n", m.maintenanceStatusCode)
	}

	m.addCORSHeadersToMaintenanceResponse(rw, req)

	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.WriteHeader(m.maintenanceStatusCode)
	_, _ = rw.Write([]byte("Service is in maintenance mode"))
}

func (m *MaintenanceCheck) addCORSHeadersToMaintenanceResponse(rw http.ResponseWriter, req *http.Request) {
	clientOrigin := req.Header.Get("Origin")
	if clientOrigin == "" {
		return
	}

	rw.Header().Set("Access-Control-Allow-Origin", clientOrigin)
	rw.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	rw.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-CSRF-Token")
	rw.Header().Set("Access-Control-Allow-Credentials", "true")
	rw.Header().Set("Access-Control-Max-Age", "86400")

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Added CORS headers to maintenance response for origin: %s\n", clientOrigin)
	}
}

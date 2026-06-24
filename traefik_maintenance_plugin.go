package traefik_maintenance_plugin

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type MaintenanceCheck struct {
	next                  http.Handler
	skipPrefixes          []string
	skipHosts             []string
	allowHTML             bool
	allowStaticExts       []string
	maintenanceStatusCode int
	debug                 bool
	allowedOrigins        []string
	corsAllowAnyOrigin    bool
}

func New(_ context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if config.MaintenanceStatusCode < 100 || config.MaintenanceStatusCode > 599 {
		return nil, fmt.Errorf("invalid maintenance status code: %d (must be between 100-599)",
			config.MaintenanceStatusCode)
	}

	cacheDuration := time.Duration(config.CacheDurationInSeconds) * time.Second
	requestTimeout := time.Duration(config.RequestTimeoutInSeconds) * time.Second
	userAgent := fmt.Sprintf("TraefikMaintenancePlugin/%s", name)

	if cacheDuration <= 0 {
		cacheDuration = 10 * time.Second
	}

	if requestTimeout <= 0 {
		requestTimeout = 5 * time.Second
	}

	environmentEndpoints := config.EnvironmentEndpoints
	if len(environmentEndpoints) == 0 {
		environmentEndpoints = map[string]string{
			"": defaultMaintenanceEndpoint,
		}
	}

	ensureSharedCacheInitialized(environmentEndpoints, config.EnvironmentSecrets, cacheDuration, requestTimeout, config.Debug, userAgent, config.SecretHeader, config.SecretHeaderValue)

	skipPrefixesCopy := make([]string, len(config.SkipPrefixes))
	copy(skipPrefixesCopy, config.SkipPrefixes)

	skipHostsCopy := make([]string, len(config.SkipHosts))
	copy(skipHostsCopy, config.SkipHosts)

	// Normalize static extensions once here (lowercase, trimmed, no empties) so
	// the per-request suffix check stays allocation-free on the hot path.
	staticExtsCopy := make([]string, 0, len(config.AllowStaticExtensions))
	for _, ext := range config.AllowStaticExtensions {
		if trimmed := strings.TrimSpace(strings.ToLower(ext)); trimmed != "" {
			staticExtsCopy = append(staticExtsCopy, trimmed)
		}
	}

	allowedOriginsCopy := make([]string, len(config.AllowedOrigins))
	copy(allowedOriginsCopy, config.AllowedOrigins)

	m := &MaintenanceCheck{
		next:                  next,
		skipPrefixes:          skipPrefixesCopy,
		skipHosts:             skipHostsCopy,
		allowHTML:             config.AllowHTMLWhenMaintenance,
		allowStaticExts:       staticExtsCopy,
		maintenanceStatusCode: config.MaintenanceStatusCode,
		debug:                 config.Debug,
		allowedOrigins:        allowedOriginsCopy,
		corsAllowAnyOrigin:    config.CorsAllowAnyOrigin,
	}

	return m, nil
}

func (m *MaintenanceCheck) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req == nil {
		http.Error(rw, "Bad Request: nil request received", http.StatusBadRequest)
		return
	}

	m.logRequestHeadersForDebugging(req)

	normalizedHost := m.extractHostWithoutPort(req.Host)

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Evaluating request: host=%s, path=%s\n", normalizedHost, req.URL.Path)
	}

	if m.isHostSkipped(normalizedHost) {
		if m.debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Host '%s' is in skip list, bypassing maintenance check\n", normalizedHost)
		}
		m.next.ServeHTTP(rw, req)
		return
	}

	if m.isPrefixSkipped(req.URL.Path) {
		if m.debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Path '%s' matches skip prefix, bypassing maintenance check\n", req.URL.Path)
		}
		m.next.ServeHTTP(rw, req)
		return
	}

	if m.handleCORSPreflightRequest(rw, req, normalizedHost) {
		return
	}

	isActive, whitelist := getMaintenanceStatusForDomain(normalizedHost)
	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Maintenance status: active=%v, whitelist=%v\n", isActive, whitelist)
	}

	if isActive {
		if m.allowHTML && isHTMLRequest(req) {
			if m.debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Maintenance active but request accepts HTML, bypassing maintenance check\n")
			}
			m.next.ServeHTTP(rw, req)
			return
		}

		if isStaticAssetRequest(req, m.allowStaticExts) {
			if m.debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Maintenance active but path '%s' matches allowed static extensions, bypassing maintenance check\n", req.URL.Path)
			}
			m.next.ServeHTTP(rw, req)
			return
		}

		if m.isClientAllowed(req, whitelist) {
			m.next.ServeHTTP(rw, req)
			return
		}

		m.sendMaintenanceResponseWithCORS(rw, req)
		return
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Maintenance mode is inactive, allowing request\n")
	}
	m.next.ServeHTTP(rw, req)
}

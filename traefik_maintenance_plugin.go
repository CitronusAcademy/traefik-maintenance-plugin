package traefik_maintenance_plugin

import (
	"context"
	"fmt"
	"net/http"
	"os"
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
}

func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
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
			".com":   "http://maintenance-service.admin/v1/configurations/",
			".world": "http://maintenance-service.stage-admin/v1/configurations/",
			".pro":   "http://maintenance-service.develop-admin/v1/configurations/",
			"":       "http://maintenance-service.admin/v1/configurations/",
		}
	}

	ensureSharedCacheInitialized(environmentEndpoints, config.EnvironmentSecrets, cacheDuration, requestTimeout, config.Debug, userAgent, config.SecretHeader, config.SecretHeaderValue)

	skipPrefixesCopy := make([]string, len(config.SkipPrefixes))
	copy(skipPrefixesCopy, config.SkipPrefixes)

	skipHostsCopy := make([]string, len(config.SkipHosts))
	copy(skipHostsCopy, config.SkipHosts)

	staticExtsCopy := make([]string, len(config.AllowStaticExtensions))
	copy(staticExtsCopy, config.AllowStaticExtensions)

	m := &MaintenanceCheck{
		next:                  next,
		skipPrefixes:          skipPrefixesCopy,
		skipHosts:             skipHostsCopy,
		allowHTML:             config.AllowHTMLWhenMaintenance,
		allowStaticExts:       staticExtsCopy,
		maintenanceStatusCode: config.MaintenanceStatusCode,
		debug:                 config.Debug,
	}

	go func() {
		<-ctx.Done()
		if config.Debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Context cancelled for middleware instance\n")
		}
	}()

	return m, nil
}

func (m *MaintenanceCheck) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req == nil {
		http.Error(rw, "Bad Request: nil request received", http.StatusBadRequest)
		return
	}

	if m.handleCORSPreflightRequest(rw, req) {
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

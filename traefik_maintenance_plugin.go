package traefik_maintenance_plugin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/cors"
	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/logx"
	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/skip"
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
	trustedProxies        []*net.IPNet
	strictAssetMatching   bool
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

	// Parse trustedProxies into CIDRs once. A bare IP becomes a host route
	// (/32 or /128). When the set is empty, Cf-Connecting-Ip is trusted
	// unconditionally (today's default behavior).
	var trustedProxies []*net.IPNet
	for _, entry := range config.TrustedProxies {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			if ip := net.ParseIP(entry); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				entry = fmt.Sprintf("%s/%d", entry, bits)
			}
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil {
			trustedProxies = append(trustedProxies, cidr)
		} else if config.Debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Warning: ignoring invalid trustedProxies entry %q: %v\n", entry, err)
		}
	}

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
		trustedProxies:        trustedProxies,
		strictAssetMatching:   config.StrictAssetMatching,
	}

	return m, nil
}

func (m *MaintenanceCheck) logRequestHeadersForDebugging(req *http.Request) {
	if !m.debug {
		return
	}

	fmt.Fprintf(logx.Out, "[MaintenanceCheck] Request headers for diagnostics:\n")
	for headerName, headerValues := range req.Header {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck]   %s: %s\n", headerName, strings.Join(headerValues, ", "))
	}

	fmt.Fprintf(logx.Out, "[MaintenanceCheck] Using only Cf-Connecting-Ip header for IP detection (supports single value or CSV)\n")
}

func (m *MaintenanceCheck) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req == nil {
		http.Error(rw, "Bad Request: nil request received", http.StatusBadRequest)
		return
	}

	m.logRequestHeadersForDebugging(req)

	normalizedHost := skip.HostWithoutPort(req.Host, m.debug)

	if m.debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Evaluating request: host=%s, path=%s\n", normalizedHost, req.URL.Path)
	}

	if skip.HostSkipped(normalizedHost, m.skipHosts, m.debug) {
		if m.debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Host '%s' is in skip list, bypassing maintenance check\n", normalizedHost)
		}
		m.next.ServeHTTP(rw, req)
		return
	}

	if skip.PrefixSkipped(req.URL.Path, m.skipPrefixes, m.debug) {
		if m.debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Path '%s' matches skip prefix, bypassing maintenance check\n", req.URL.Path)
		}
		m.next.ServeHTTP(rw, req)
		return
	}

	if m.handleCORSPreflightRequest(rw, req, normalizedHost) {
		return
	}

	isActive, whitelist := getMaintenanceStatusForDomain(normalizedHost)
	if m.debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Maintenance status: active=%v, whitelist=%v\n", isActive, whitelist)
	}

	if isActive {
		if m.allowHTML && skip.IsHTMLRequest(req) {
			if m.debug {
				fmt.Fprintf(logx.Out, "[MaintenanceCheck] Maintenance active but request accepts HTML, bypassing maintenance check\n")
			}
			m.next.ServeHTTP(rw, req)
			return
		}

		if skip.IsStaticAsset(req, m.allowStaticExts, m.strictAssetMatching) {
			if m.debug {
				fmt.Fprintf(logx.Out, "[MaintenanceCheck] Maintenance active but path '%s' matches allowed static extensions, bypassing maintenance check\n", req.URL.Path)
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
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Maintenance mode is inactive, allowing request\n")
	}
	m.next.ServeHTTP(rw, req)
}

func (m *MaintenanceCheck) handleCORSPreflightRequest(rw http.ResponseWriter, req *http.Request, normalizedHost string) bool {
	if req.Method != http.MethodOptions {
		return false
	}

	isActive, whitelist := getMaintenanceStatusForDomain(normalizedHost)
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
	cors.WriteHeaders(rw, origin, m.allowedOrigins, m.corsAllowAnyOrigin, m.debug)
}

func (m *MaintenanceCheck) sendBlockedPreflightResponse(rw http.ResponseWriter) {
	if m.debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] CORS preflight completed, but actual request will be blocked due to maintenance mode\n")
	}

	// Preflight must always return 2xx status according to CORS spec
	rw.WriteHeader(http.StatusOK)
}

func (m *MaintenanceCheck) sendSuccessfulPreflightResponse(rw http.ResponseWriter) {
	rw.WriteHeader(http.StatusNoContent)
}

func (m *MaintenanceCheck) sendMaintenanceResponseWithCORS(rw http.ResponseWriter, req *http.Request) {
	if m.debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Access denied, returning status code %d\n", m.maintenanceStatusCode)
	}

	m.addCORSHeadersToMaintenanceResponse(rw, req)

	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.WriteHeader(m.maintenanceStatusCode)
	_, _ = rw.Write([]byte("Service is in maintenance mode"))
}

func (m *MaintenanceCheck) addCORSHeadersToMaintenanceResponse(rw http.ResponseWriter, req *http.Request) {
	cors.WriteHeaders(rw, req.Header.Get("Origin"), m.allowedOrigins, m.corsAllowAnyOrigin, m.debug)
}

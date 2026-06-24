package traefik_maintenance_plugin

import (
	"context"
	"fmt"
	"net"
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

func (m *MaintenanceCheck) handleCORSPreflightRequest(rw http.ResponseWriter, req *http.Request) bool {
	if req.Method != http.MethodOptions {
		return false
	}

	isActive, whitelist := getMaintenanceStatusForDomain(req.Host)
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

func (m *MaintenanceCheck) logRequestHeadersForDebugging(req *http.Request) {
	if !m.debug {
		return
	}

	fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Request headers for diagnostics:\n")
	for headerName, headerValues := range req.Header {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck]   %s: %s\n", headerName, strings.Join(headerValues, ", "))
	}

	fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Using only Cf-Connecting-Ip header for IP detection (supports single value or CSV)\n")
}

func (m *MaintenanceCheck) extractHostWithoutPort(originalHost string) string {
	host := originalHost
	if colonIndex := strings.IndexByte(host, ':'); colonIndex > 0 {
		host = host[:colonIndex]
		if m.debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Normalized host from '%s' to '%s'\n", originalHost, host)
		}
	}
	return host
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

// getAllClientIPs extracts all IP addresses from Cf-Connecting-Ip header only
// Returns a slice of unique IPs found in the header (supports single value or CSV)
func getAllClientIPs(req *http.Request, debug bool) []string {
	// Guard against nil request
	if req == nil {
		return []string{}
	}

	// Use only Cf-Connecting-Ip header (CloudFlare)
	const cfHeader = "Cf-Connecting-Ip"

	// Use a map to track unique IPs
	uniqueIPs := make(map[string]struct{})
	var allIPs []string

	if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Extracting client IPs from %s header only\n", cfHeader)
	}

	// Get Cf-Connecting-Ip header value
	addresses := req.Header.Get(cfHeader)
	if addresses != "" {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Processing header %s with value: %s\n", cfHeader, addresses)
		}

		// Handle comma-separated values (CSV) or single value
		parts := strings.Split(addresses, ",")
		for _, part := range parts {
			ip := strings.TrimSpace(part)
			// Handle IPv6 bracket notation [IPv6]:port
			ip = cleanIPAddress(ip)
			if ip != "" {
				if _, exists := uniqueIPs[ip]; !exists {
					uniqueIPs[ip] = struct{}{}
					allIPs = append(allIPs, ip)
					if debug {
						fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Extracted IP from %s header: %s\n", cfHeader, ip)
					}
				}
			}
		}
	} else if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] %s header not found or empty\n", cfHeader)
	}

	if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Total unique IPs extracted: %d - %v\n", len(allIPs), allIPs)
	}

	return allIPs
}

// cleanIPAddress removes brackets, ports, and whitespace from an IP address
func cleanIPAddress(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}

	// Handle IPv6 bracket notation [IPv6]:port or [IPv6]
	if strings.HasPrefix(ip, "[") {
		if bracketEnd := strings.Index(ip, "]"); bracketEnd != -1 {
			ip = ip[1:bracketEnd]
		}
	} else if strings.Contains(ip, ".") {
		// IPv4 with possible port
		if colonIdx := strings.LastIndex(ip, ":"); colonIdx != -1 {
			// Make sure this is not IPv6 (IPv6 has multiple colons)
			if strings.Count(ip, ":") == 1 {
				ip = ip[:colonIdx]
			}
		}
	}

	return ip
}

func (m *MaintenanceCheck) isClientAllowed(req *http.Request, whitelist []string) bool {
	// Guard against nil request or whitelist
	if req == nil {
		return false
	}

	if len(whitelist) == 0 {
		if m.debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Whitelist is empty, blocking request\n")
		}
		return false
	}

	// Extended debug info for whitelist
	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Maintenance whitelist entries: %d items\n", len(whitelist))
		for i, entry := range whitelist {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck]   Whitelist[%d]: %s\n", i, entry)
		}
	}

	// Check for wildcard first
	for _, entry := range whitelist {
		if entry == "*" {
			if m.debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Wildcard (*) found in whitelist, allowing request\n")
			}
			return true
		}
	}

	// Get ALL client IPs from all headers
	clientIPs := getAllClientIPs(req, m.debug)

	// No IPs found at all
	if len(clientIPs) == 0 {
		if m.debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Could not determine any client IPs, blocking request\n")
		}
		return false
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Client IPs for whitelist check: %v\n", clientIPs)
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Beginning whitelist evaluation for %d IPs\n", len(clientIPs))
	}

	// Check each client IP against the whitelist
	// If ANY IP matches, allow the request
	for _, clientIP := range clientIPs {
		if clientIP == "" {
			continue
		}

		for _, whitelistEntry := range whitelist {
			// Exact match (IPv6-canonical via net.ParseIP; non-IP entries compared raw)
			if ipExactMatch(clientIP, whitelistEntry) {
				if m.debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] IP '%s' matches whitelist entry '%s', allowing request\n", clientIP, whitelistEntry)
				}
				return true
			}

			// CIDR notation match
			if strings.Contains(whitelistEntry, "/") {
				if m.debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Checking if IP '%s' is in CIDR range '%s'\n", clientIP, whitelistEntry)
				}

				match, err := isCIDRMatch(clientIP, whitelistEntry)
				if err != nil {
					if m.debug {
						fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Error checking CIDR match: %v\n", err)
					}
				} else if match {
					if m.debug {
						fmt.Fprintf(os.Stdout, "[MaintenanceCheck] IP '%s' is in CIDR range '%s', allowing request\n", clientIP, whitelistEntry)
					}
					return true
				} else if m.debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] IP '%s' is NOT in CIDR range '%s'\n", clientIP, whitelistEntry)
				}
			}
		}
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] None of the client IPs %v matched whitelist, blocking request\n", clientIPs)
	}
	return false
}

// isHTMLRequest returns true when the request explicitly accepts HTML content.
// This is used to optionally let base web pages through during maintenance,
// while keeping API calls (typically JSON) blocked.
func isHTMLRequest(req *http.Request) bool {
	if req == nil {
		return false
	}

	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}

	acceptHeader := req.Header.Get("Accept")
	if acceptHeader == "" {
		return false
	}

	// Check if any Accept entry contains text/html
	for _, part := range strings.Split(acceptHeader, ",") {
		if strings.Contains(strings.ToLower(strings.TrimSpace(part)), "text/html") {
			return true
		}
	}

	return false
}

// isStaticAssetRequest returns true for GET/HEAD requests whose URL path ends
// with one of the configured static extensions (case-insensitive). This is
// used to optionally let static assets (JS/CSS/images/fonts, etc.) through
// during maintenance without whitelisting.
func isStaticAssetRequest(req *http.Request, extensions []string) bool {
	if req == nil {
		return false
	}

	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}

	if len(extensions) == 0 {
		return false
	}

	path := strings.ToLower(req.URL.Path)
	for _, ext := range extensions {
		trimmed := strings.TrimSpace(strings.ToLower(ext))
		if trimmed == "" {
			continue
		}
		if strings.HasSuffix(path, trimmed) {
			return true
		}
	}

	return false
}

func (m *MaintenanceCheck) isHostSkipped(host string) bool {
	// Guard against empty hosts
	if host == "" {
		return false
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Checking host '%s' against skipHosts: %v\n", host, m.skipHosts)
	}

	for _, skipHost := range m.skipHosts {
		// Skip empty entries
		if skipHost == "" {
			continue
		}

		// Check for wildcard domain pattern (*.example.com)
		if strings.HasPrefix(skipHost, "*.") {
			suffix := skipHost[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) {
				if m.debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Host '%s' matches wildcard pattern '%s'\n", host, skipHost)
				}
				return true
			}
		} else if skipHost == host {
			if m.debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Host '%s' matches exact host '%s'\n", host, skipHost)
			}
			return true
		}
	}

	return false
}

func (m *MaintenanceCheck) isPrefixSkipped(path string) bool {
	// Guard against nil path
	if path == "" {
		return false
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Checking path '%s' against skipPrefixes: %v\n", path, m.skipPrefixes)
	}

	for _, prefix := range m.skipPrefixes {
		// Skip empty prefixes
		if prefix == "" {
			continue
		}

		if strings.HasPrefix(path, prefix) {
			if m.debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Path '%s' matches prefix '%s'\n", path, prefix)
			}
			return true
		}
	}

	return false
}

// ipExactMatch reports whether entry and clientIP denote the same address.
// Both are parsed with net.ParseIP so any textual IPv6 form (letter case,
// zero-compression, full expansion) matches; non-IP entries fall back to a
// raw string compare.
func ipExactMatch(clientIP, entry string) bool {
	ce := net.ParseIP(entry)
	ci := net.ParseIP(clientIP)
	if ce != nil && ci != nil {
		return ce.Equal(ci)
	}
	return entry == clientIP
}

// isCIDRMatch checks if an IP is contained within a CIDR range
func isCIDRMatch(ip, cidr string) (bool, error) {
	// Parse the CIDR notation
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false, fmt.Errorf("invalid CIDR notation %s: %v", cidr, err)
	}

	// Parse the IP
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false, fmt.Errorf("invalid IP address %s (cannot be parsed as IPv4 or IPv6)", ip)
	}

	// Check if the IP is the correct version (IPv4/IPv6) for the CIDR
	if (ipNet.IP.To4() == nil) != (parsedIP.To4() == nil) {
		return false, fmt.Errorf("IP version mismatch: CIDR %s is %s but IP %s is %s",
			cidr,
			ipVersionName(ipNet.IP),
			ip,
			ipVersionName(parsedIP))
	}

	// Check if the IP is contained in the CIDR range
	return ipNet.Contains(parsedIP), nil
}

// ipVersionName returns a string indicating whether an IP is IPv4 or IPv6
func ipVersionName(ip net.IP) string {
	if ip.To4() != nil {
		return "IPv4"
	}
	return "IPv6"
}

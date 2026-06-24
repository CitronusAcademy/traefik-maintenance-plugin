package traefik_maintenance_plugin

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

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

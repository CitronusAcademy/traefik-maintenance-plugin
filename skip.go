package traefik_maintenance_plugin

import (
	"fmt"
	"net"
	"net/http"
	"path"
	"strings"
)

func (m *MaintenanceCheck) logRequestHeadersForDebugging(req *http.Request) {
	if !m.debug {
		return
	}

	fmt.Fprintf(logOut, "[MaintenanceCheck] Request headers for diagnostics:\n")
	for headerName, headerValues := range req.Header {
		fmt.Fprintf(logOut, "[MaintenanceCheck]   %s: %s\n", headerName, strings.Join(headerValues, ", "))
	}

	fmt.Fprintf(logOut, "[MaintenanceCheck] Using only Cf-Connecting-Ip header for IP detection (supports single value or CSV)\n")
}

func (m *MaintenanceCheck) extractHostWithoutPort(originalHost string) string {
	if originalHost == "" {
		return ""
	}

	host := originalHost
	if h, _, err := net.SplitHostPort(originalHost); err == nil {
		// "host:port", "[v6]:port" → bare host / bare v6
		host = h
	} else if strings.HasPrefix(host, "[") {
		// "[v6]" with no port → strip brackets
		if end := strings.Index(host, "]"); end != -1 {
			host = host[1:end]
		}
	}
	// Otherwise (bare hostname with no port, or bare IPv6 with no brackets)
	// SplitHostPort errors and we keep the original — splitting on ':' here
	// would corrupt a bare IPv6 address.

	if m.debug && host != originalHost {
		fmt.Fprintf(logOut, "[MaintenanceCheck] Normalized host from '%s' to '%s'\n", originalHost, host)
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
// during maintenance without whitelisting. The extensions are already
// normalized (lowercased, trimmed, no empties) at construction time.
//
// When strict is false (the default) the whole path is suffix-matched, so any
// path crafted to end in an allowed extension (e.g. "/api/export.css") is let
// through. When strict is true only a genuine file extension on the last path
// segment is matched (via path.Ext), closing that bypass at the cost of also
// requiring real asset paths to carry their extension on the final segment.
func isStaticAssetRequest(req *http.Request, extensions []string, strict bool) bool {
	if req == nil {
		return false
	}

	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}

	if len(extensions) == 0 {
		return false
	}

	if strict {
		// Only the last path segment's real extension counts.
		ext := strings.ToLower(path.Ext(req.URL.Path))
		if ext == "" {
			return false
		}
		for _, allowed := range extensions {
			if ext == allowed {
				return true
			}
		}
		return false
	}

	pathLower := strings.ToLower(req.URL.Path)
	for _, ext := range extensions {
		if strings.HasSuffix(pathLower, ext) {
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
		fmt.Fprintf(logOut, "[MaintenanceCheck] Checking host '%s' against skipHosts: %v\n", host, m.skipHosts)
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
					fmt.Fprintf(logOut, "[MaintenanceCheck] Host '%s' matches wildcard pattern '%s'\n", host, skipHost)
				}
				return true
			}
		} else if skipHost == host {
			if m.debug {
				fmt.Fprintf(logOut, "[MaintenanceCheck] Host '%s' matches exact host '%s'\n", host, skipHost)
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
		fmt.Fprintf(logOut, "[MaintenanceCheck] Checking path '%s' against skipPrefixes: %v\n", path, m.skipPrefixes)
	}

	for _, prefix := range m.skipPrefixes {
		// Skip empty prefixes
		if prefix == "" {
			continue
		}

		if strings.HasPrefix(path, prefix) {
			if m.debug {
				fmt.Fprintf(logOut, "[MaintenanceCheck] Path '%s' matches prefix '%s'\n", path, prefix)
			}
			return true
		}
	}

	return false
}

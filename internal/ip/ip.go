// Package ip resolves the client IP from the Cf-Connecting-Ip header and
// matches it against a maintenance whitelist (exact, CIDR, or wildcard),
// optionally gated by a set of trusted proxy networks.
package ip

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/logx"
)

// IsClientAllowed reports whether the request's client IP (from Cf-Connecting-Ip)
// satisfies the whitelist. When trustedProxies is non-empty, the header is only
// honored if the request's immediate peer is within one of those networks;
// otherwise the spoofable header is ignored and the request is blocked.
func IsClientAllowed(req *http.Request, whitelist []string, trustedProxies []*net.IPNet, debug bool) bool {
	// Guard against nil request or whitelist
	if req == nil {
		return false
	}

	if len(whitelist) == 0 {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Whitelist is empty, blocking request\n")
		}
		return false
	}

	// Extended debug info for whitelist
	if debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Maintenance whitelist entries: %d items\n", len(whitelist))
		for i, entry := range whitelist {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck]   Whitelist[%d]: %s\n", i, entry)
		}
	}

	// Check for wildcard first
	if hasWildcard(whitelist, debug) {
		return true
	}

	// When trustedProxies is configured, only honor Cf-Connecting-Ip if the
	// request's immediate peer is a trusted hop; otherwise the header is
	// spoofable and we resolve no client IP (block during maintenance).
	if !remoteAddrTrusted(req, trustedProxies) {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Request peer %s is not a trusted proxy; ignoring Cf-Connecting-Ip\n", req.RemoteAddr)
		}
		return false
	}

	// Get ALL client IPs from all headers
	clientIPs := getAllClientIPs(req, debug)

	// No IPs found at all
	if len(clientIPs) == 0 {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Could not determine any client IPs, blocking request\n")
		}
		return false
	}

	if debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Client IPs for whitelist check: %v\n", clientIPs)
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Beginning whitelist evaluation for %d IPs\n", len(clientIPs))
	}

	// Check each client IP against the whitelist; if ANY matches, allow.
	if anyIPMatchesWhitelist(clientIPs, whitelist, debug) {
		return true
	}

	if debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] None of the client IPs %v matched whitelist, blocking request\n", clientIPs)
	}
	return false
}

// hasWildcard reports whether the whitelist contains the "*" entry, which
// allows every client.
func hasWildcard(whitelist []string, debug bool) bool {
	for _, entry := range whitelist {
		if entry == "*" {
			if debug {
				fmt.Fprintf(logx.Out, "[MaintenanceCheck] Wildcard (*) found in whitelist, allowing request\n")
			}
			return true
		}
	}
	return false
}

// anyIPMatchesWhitelist reports whether any of clientIPs satisfies any whitelist
// entry (exact or CIDR). The "*" wildcard is handled by the caller.
func anyIPMatchesWhitelist(clientIPs, whitelist []string, debug bool) bool {
	for _, clientIP := range clientIPs {
		if clientIP == "" {
			continue
		}
		for _, whitelistEntry := range whitelist {
			if matchWhitelistEntry(clientIP, whitelistEntry, debug) {
				return true
			}
		}
	}
	return false
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
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Extracting client IPs from %s header only\n", cfHeader)
	}

	// Get Cf-Connecting-Ip header value
	addresses := req.Header.Get(cfHeader)
	if addresses != "" {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Processing header %s with value: %s\n", cfHeader, addresses)
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
						fmt.Fprintf(logx.Out, "[MaintenanceCheck] Extracted IP from %s header: %s\n", cfHeader, ip)
					}
				}
			}
		}
	} else if debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] %s header not found or empty\n", cfHeader)
	}

	if debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Total unique IPs extracted: %d - %v\n", len(allIPs), allIPs)
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

// remoteAddrTrusted reports whether the request's immediate peer is within the
// configured trustedProxies set. When no proxies are configured it returns true
// (Cf-Connecting-Ip is trusted unconditionally — the default).
func remoteAddrTrusted(req *http.Request, trustedProxies []*net.IPNet) bool {
	if len(trustedProxies) == 0 {
		return true
	}
	host := req.RemoteAddr
	if h, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range trustedProxies {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// matchWhitelistEntry reports whether clientIP satisfies a single whitelist
// entry: an exact IP match (IPv6-canonical via ipExactMatch) or, for an entry
// in CIDR notation, a range match. It does not handle the "*" wildcard — the
// caller checks that before iterating entries.
func matchWhitelistEntry(clientIP, whitelistEntry string, debug bool) bool {
	// Exact match (IPv6-canonical via net.ParseIP; non-IP entries compared raw)
	if ipExactMatch(clientIP, whitelistEntry) {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] IP '%s' matches whitelist entry '%s', allowing request\n", clientIP, whitelistEntry)
		}
		return true
	}

	// CIDR notation match
	if strings.Contains(whitelistEntry, "/") {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Checking if IP '%s' is in CIDR range '%s'\n", clientIP, whitelistEntry)
		}

		match, err := isCIDRMatch(clientIP, whitelistEntry)
		if err != nil {
			if debug {
				fmt.Fprintf(logx.Out, "[MaintenanceCheck] Error checking CIDR match: %v\n", err)
			}
		} else if match {
			if debug {
				fmt.Fprintf(logx.Out, "[MaintenanceCheck] IP '%s' is in CIDR range '%s', allowing request\n", clientIP, whitelistEntry)
			}
			return true
		} else if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] IP '%s' is NOT in CIDR range '%s'\n", clientIP, whitelistEntry)
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

// isCIDRMatch checks if an IP is contained within a CIDR range. An IP/CIDR
// version mismatch (IPv4 IP vs IPv6 range or vice-versa) is not special-cased:
// net.IPNet.Contains already returns false for it.
func isCIDRMatch(ipStr, cidr string) (bool, error) {
	// Parse the CIDR notation
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false, fmt.Errorf("invalid CIDR notation %s: %w", cidr, err)
	}

	// Parse the IP
	parsedIP := net.ParseIP(ipStr)
	if parsedIP == nil {
		return false, fmt.Errorf("invalid IP address %s (cannot be parsed as IPv4 or IPv6)", ipStr)
	}

	// Check if the IP is contained in the CIDR range
	return ipNet.Contains(parsedIP), nil
}

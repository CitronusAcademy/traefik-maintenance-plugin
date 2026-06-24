package traefik_maintenance_plugin

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

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
		return false, fmt.Errorf("invalid CIDR notation %s: %w", cidr, err)
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

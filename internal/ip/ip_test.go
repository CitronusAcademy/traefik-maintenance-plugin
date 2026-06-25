package ip

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedProxiesBlocksSpoofedHeader(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("10.0.0.0/8")
	trusted := []*net.IPNet{cidr}

	// Request arrives from an UNTRUSTED hop with a spoofed whitelisted IP.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:5555" // not in 10.0.0.0/8
	req.Header.Set("Cf-Connecting-Ip", "192.168.1.1")
	if IsClientAllowed(req, []string{"192.168.1.1"}, trusted, false) {
		t.Fatal("spoofed header from an untrusted proxy must not match the whitelist")
	}

	// Same header from a TRUSTED hop is honored.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.1.2.3:5555"
	req2.Header.Set("Cf-Connecting-Ip", "192.168.1.1")
	if !IsClientAllowed(req2, []string{"192.168.1.1"}, trusted, false) {
		t.Fatal("header from a trusted proxy should be honored")
	}
}

func TestIPVersionName(t *testing.T) {
	if got := ipVersionName(net.ParseIP("1.2.3.4")); got != "IPv4" {
		t.Errorf("IPv4: got %q", got)
	}
	if got := ipVersionName(net.ParseIP("2001:db8::1")); got != "IPv6" {
		t.Errorf("IPv6: got %q", got)
	}
}

func TestCleanIPAddressForms(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":           "1.2.3.4",
		"1.2.3.4:8080":      "1.2.3.4",
		"[2001:db8::1]:443": "2001:db8::1",
		"[2001:db8::1]":     "2001:db8::1",
		"2001:db8::1":       "2001:db8::1",
		"  1.2.3.4  ":       "1.2.3.4",
		"":                  "",
	}
	for in, want := range cases {
		if got := cleanIPAddress(in); got != want {
			t.Errorf("cleanIPAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsClientAllowedCIDRAndExact(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Cf-Connecting-Ip", "10.0.0.5")

	if !IsClientAllowed(req, []string{"10.0.0.0/16"}, nil, false) {
		t.Error("IP within CIDR must be allowed")
	}
	if IsClientAllowed(req, []string{"192.168.0.0/16"}, nil, false) {
		t.Error("IP outside CIDR must be blocked")
	}
	if IsClientAllowed(req, []string{"not-a-cidr/xx"}, nil, false) {
		t.Error("invalid CIDR entry must not allow")
	}
	if !IsClientAllowed(req, []string{"10.0.0.5"}, nil, false) {
		t.Error("exact IP must be allowed")
	}
	if IsClientAllowed(req, []string{}, nil, false) {
		t.Error("empty whitelist must block")
	}
}

// TestIsClientAllowedDebugBranches exercises the debug=true diagnostic paths
// (wildcard, untrusted-peer block, no-IP block, and the whitelist-entry match
// logging) so the package's own coverage profile covers them.
func TestIsClientAllowedDebugBranches(t *testing.T) {
	// Wildcard match with debug on.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cf-Connecting-Ip", "8.8.8.8")
	if !IsClientAllowed(req, []string{"*"}, nil, true) {
		t.Error("wildcard must allow")
	}

	// Untrusted peer with debug on → blocked.
	_, cidr, _ := net.ParseCIDR("10.0.0.0/8")
	untrusted := httptest.NewRequest(http.MethodGet, "/", nil)
	untrusted.RemoteAddr = "203.0.113.9:5555"
	untrusted.Header.Set("Cf-Connecting-Ip", "1.2.3.4")
	if IsClientAllowed(untrusted, []string{"1.2.3.4"}, []*net.IPNet{cidr}, true) {
		t.Error("untrusted peer must block")
	}

	// No Cf-Connecting-Ip header with debug on → no IPs → blocked.
	noip := httptest.NewRequest(http.MethodGet, "/", nil)
	if IsClientAllowed(noip, []string{"1.2.3.4"}, nil, true) {
		t.Error("missing header must block")
	}

	// Exact + CIDR-miss diagnostics with debug on.
	hit := httptest.NewRequest(http.MethodGet, "/", nil)
	hit.Header.Set("Cf-Connecting-Ip", "10.0.0.5")
	if !IsClientAllowed(hit, []string{"192.168.0.0/16", "10.0.0.5"}, nil, true) {
		t.Error("exact match after CIDR miss must allow")
	}

	// nil request guard.
	if IsClientAllowed(nil, []string{"*"}, nil, false) {
		t.Error("nil request must block")
	}
	// getAllClientIPs nil guard (debug on path is in IsClientAllowed; cover the
	// direct nil-request return here).
	if got := getAllClientIPs(nil, true); len(got) != 0 {
		t.Errorf("nil request must yield no IPs, got %v", got)
	}
}

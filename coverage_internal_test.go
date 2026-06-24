package traefik_maintenance_plugin

import (
	"net"
	"net/http"
	"testing"
	"time"
)

func TestIPVersionName(t *testing.T) {
	if got := ipVersionName(net.ParseIP("1.2.3.4")); got != "IPv4" {
		t.Errorf("IPv4: got %q", got)
	}
	if got := ipVersionName(net.ParseIP("2001:db8::1")); got != "IPv6" {
		t.Errorf("IPv6: got %q", got)
	}
}

func TestCalculateBackoff(t *testing.T) {
	if d := calculateBackoff(0); d != 5*time.Second {
		t.Errorf("attempts=0: got %v, want 5s", d)
	}
	if d := calculateBackoff(-3); d != 5*time.Second {
		t.Errorf("negative attempts: got %v, want 5s", d)
	}
	// attempts=1 → base 10s ± 20% jitter.
	if d := calculateBackoff(1); d < 8*time.Second || d > 12*time.Second {
		t.Errorf("attempts=1: got %v, want within [8s,12s]", d)
	}
	// Large attempts are capped at 1h.
	if d := calculateBackoff(50); d != time.Hour {
		t.Errorf("attempts=50: got %v, want 1h cap", d)
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

func TestSkipHelpersEdges(t *testing.T) {
	m := &MaintenanceCheck{
		skipHosts:    []string{"", "exact.example.com", "*.internal.example.com"},
		skipPrefixes: []string{"", "/admin"},
	}
	if m.isHostSkipped("") {
		t.Error("empty host must not be skipped")
	}
	if !m.isHostSkipped("exact.example.com") {
		t.Error("exact host must be skipped")
	}
	if !m.isHostSkipped("a.internal.example.com") {
		t.Error("wildcard host must be skipped")
	}
	if m.isHostSkipped("other.example.com") {
		t.Error("non-matching host must not be skipped")
	}
	if m.isPrefixSkipped("") {
		t.Error("empty path must not be skipped")
	}
	if !m.isPrefixSkipped("/admin/users") {
		t.Error("prefix must be skipped")
	}
	if m.isPrefixSkipped("/public") {
		t.Error("non-matching prefix must not be skipped")
	}
}

func TestLogRequestHeadersForDebugging(t *testing.T) {
	// debug on: exercises the logging branch (output goes to stdout; we only
	// assert it does not panic and the debug-off early return is also covered).
	on := &MaintenanceCheck{debug: true}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Authorization", "Bearer x")
	on.logRequestHeadersForDebugging(req)

	off := &MaintenanceCheck{debug: false}
	off.logRequestHeadersForDebugging(req)
}

func TestIsClientAllowedCIDRAndExact(t *testing.T) {
	m := &MaintenanceCheck{}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Cf-Connecting-Ip", "10.0.0.5")

	if !m.isClientAllowed(req, []string{"10.0.0.0/16"}) {
		t.Error("IP within CIDR must be allowed")
	}
	if m.isClientAllowed(req, []string{"192.168.0.0/16"}) {
		t.Error("IP outside CIDR must be blocked")
	}
	if m.isClientAllowed(req, []string{"not-a-cidr/xx"}) {
		t.Error("invalid CIDR entry must not allow")
	}
	if !m.isClientAllowed(req, []string{"10.0.0.5"}) {
		t.Error("exact IP must be allowed")
	}
	if m.isClientAllowed(req, []string{}) {
		t.Error("empty whitelist must block")
	}
}

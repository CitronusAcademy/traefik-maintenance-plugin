package traefik_maintenance_plugin

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedProxiesBlocksSpoofedHeader(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("10.0.0.0/8")
	m := &MaintenanceCheck{trustedProxies: []*net.IPNet{cidr}}

	// Request arrives from an UNTRUSTED hop with a spoofed whitelisted IP.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:5555" // not in 10.0.0.0/8
	req.Header.Set("Cf-Connecting-Ip", "192.168.1.1")
	if m.isClientAllowed(req, []string{"192.168.1.1"}) {
		t.Fatal("spoofed header from an untrusted proxy must not match the whitelist")
	}

	// Same header from a TRUSTED hop is honored.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.1.2.3:5555"
	req2.Header.Set("Cf-Connecting-Ip", "192.168.1.1")
	if !m.isClientAllowed(req2, []string{"192.168.1.1"}) {
		t.Fatal("header from a trusted proxy should be honored")
	}
}

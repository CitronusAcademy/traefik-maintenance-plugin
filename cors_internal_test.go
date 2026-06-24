package traefik_maintenance_plugin

import "testing"

func TestCORSEmptyListCanFailClosed(t *testing.T) {
	m := &MaintenanceCheck{allowedOrigins: nil, corsAllowAnyOrigin: false}
	if m.originAllowed("https://anything.example") {
		t.Fatal("with corsAllowAnyOrigin=false and an empty allow-list, no origin should be allowed")
	}
	// And the back-compat default still reflects any origin:
	m2 := &MaintenanceCheck{allowedOrigins: nil, corsAllowAnyOrigin: true}
	if !m2.originAllowed("https://anything.example") {
		t.Fatal("default corsAllowAnyOrigin=true must reflect any origin")
	}
}

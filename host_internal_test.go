package traefik_maintenance_plugin

import "testing"

func TestExtractHostWithoutPort(t *testing.T) {
	m := &MaintenanceCheck{}
	cases := []struct {
		in, want string
	}{
		{"example.world", "example.world"},
		{"example.world:443", "example.world"},
		{"", ""},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"[::1]:8080", "::1"},
		{"2001:db8::1", "2001:db8::1"}, // bare IPv6, no port: leave intact
	}
	for _, c := range cases {
		if got := m.extractHostWithoutPort(c.in); got != c.want {
			t.Errorf("extractHostWithoutPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

package skip

import "testing"

func TestHostWithoutPort(t *testing.T) {
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
		if got := HostWithoutPort(c.in, false); got != c.want {
			t.Errorf("HostWithoutPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// debug=true with a host that actually normalizes exercises the diagnostic branch.
	if got := HostWithoutPort("example.world:443", true); got != "example.world" {
		t.Errorf("HostWithoutPort with debug = %q, want %q", got, "example.world")
	}
}

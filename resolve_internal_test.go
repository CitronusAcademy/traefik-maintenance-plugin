package traefik_maintenance_plugin

import "testing"

func TestResolveEnvSuffixLongestMatchDeterministic(t *testing.T) {
	endpoints := map[string]string{
		".com":       "a",
		".world.com": "b",
		".world":     "c",
		"":           "default",
	}
	cases := []struct {
		domain, want string
	}{
		{"foo.world.com", ".world.com"}, // longest wins, not ".com"
		{"foo.com", ".com"},
		{"foo.world", ".world"},
		{"localhost", ""}, // no named suffix → default key
	}
	// Run many times: map iteration order is randomized, result must be stable.
	for _, c := range cases {
		for i := 0; i < 50; i++ {
			if got := resolveEnvSuffix(c.domain, endpoints); got != c.want {
				t.Fatalf("resolveEnvSuffix(%q) = %q, want %q", c.domain, got, c.want)
			}
		}
	}
}

func TestResolveEnvSuffixLabelBoundary(t *testing.T) {
	endpoints := map[string]string{"pro": "P", ".world": "W", "": "D"}
	cases := map[string]string{
		"mypro":    "",       // mid-label, must NOT match "pro"
		"app.pro":  "pro",    // label boundary before "pro"
		"x.world":  ".world", // leading-dot key
		"notworld": "",       // mid-label, must NOT match ".world"
		"pro":      "pro",    // whole-host equality
	}
	for host, want := range cases {
		if got := resolveEnvSuffix(host, endpoints); got != want {
			t.Errorf("resolveEnvSuffix(%q) = %q, want %q", host, got, want)
		}
	}
}

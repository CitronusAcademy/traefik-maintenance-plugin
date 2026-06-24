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

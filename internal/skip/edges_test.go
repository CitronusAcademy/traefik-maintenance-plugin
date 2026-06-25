package skip

import "testing"

func TestSkipHelpersEdges(t *testing.T) {
	skipHosts := []string{"", "exact.example.com", "*.internal.example.com"}
	skipPrefixes := []string{"", "/admin"}

	if HostSkipped("", skipHosts, false) {
		t.Error("empty host must not be skipped")
	}
	if !HostSkipped("exact.example.com", skipHosts, false) {
		t.Error("exact host must be skipped")
	}
	if !HostSkipped("a.internal.example.com", skipHosts, false) {
		t.Error("wildcard host must be skipped")
	}
	if HostSkipped("other.example.com", skipHosts, false) {
		t.Error("non-matching host must not be skipped")
	}
	if PrefixSkipped("", skipPrefixes, false) {
		t.Error("empty path must not be skipped")
	}
	if !PrefixSkipped("/admin/users", skipPrefixes, false) {
		t.Error("prefix must be skipped")
	}
	if PrefixSkipped("/public", skipPrefixes, false) {
		t.Error("non-matching prefix must not be skipped")
	}

	// debug=true on matching entries exercises the diagnostic branches.
	if !HostSkipped("exact.example.com", skipHosts, true) {
		t.Error("exact host must be skipped (debug)")
	}
	if !HostSkipped("a.internal.example.com", skipHosts, true) {
		t.Error("wildcard host must be skipped (debug)")
	}
	if !PrefixSkipped("/admin/users", skipPrefixes, true) {
		t.Error("prefix must be skipped (debug)")
	}
}

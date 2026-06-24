package traefik_maintenance_plugin

import "net/http"

// nopHandler is a no-op downstream handler for constructing the middleware in
// internal tests. The external test package has its own nextNoop helper in
// testhelpers_test.go; internal tests cannot reach it across the package
// boundary, so this is the internal equivalent.
func nopHandler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

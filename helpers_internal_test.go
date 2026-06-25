package traefik_maintenance_plugin

import (
	"bytes"
	"net/http"
	"testing"
)

// nopHandler is a no-op downstream handler for constructing the middleware in
// internal tests. The external test package has its own nextNoop helper in
// testhelpers_test.go; internal tests cannot reach it across the package
// boundary, so this is the internal equivalent.
func nopHandler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

// captureStdout redirects the package log writer for the duration of fn and
// returns whatever fn wrote there. Test-only; mutates the package-global
// logOut, so callers must not run in parallel. Swapping logOut (not os.Stdout)
// keeps this runnable under Yaegi.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := logOut
	var buf bytes.Buffer
	logOut = &buf
	defer func() { logOut = orig }()
	fn()
	return buf.String()
}

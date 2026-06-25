package traefik_maintenance_plugin

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/logx"
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
// logx.Out, so callers must not run in parallel. Swapping logx.Out (not os.Stdout)
// keeps this runnable under Yaegi.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := logx.Out
	var buf bytes.Buffer
	logx.Out = &buf
	defer func() { logx.Out = orig }()
	fn()
	return buf.String()
}

package traefik_maintenance_plugin

import (
	"io"
	"net/http"
	"os"
	"testing"
)

// nopHandler is a no-op downstream handler for constructing the middleware in
// internal tests. The external test package has its own nextNoop helper in
// testhelpers_test.go; internal tests cannot reach it across the package
// boundary, so this is the internal equivalent.
func nopHandler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

// captureStdout redirects os.Stdout for the duration of fn and returns whatever
// fn wrote there. Test-only; it mutates the global os.Stdout, so callers must
// not run in parallel. A reader goroutine drains the pipe to avoid blocking
// when fn writes more than the pipe buffer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		done <- string(buf)
	}()

	fn()

	_ = w.Close()
	os.Stdout = orig
	return <-done
}

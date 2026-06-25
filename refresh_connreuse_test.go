package traefik_maintenance_plugin_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	plugin "github.com/CitronusAcademy/traefik-maintenance-plugin"
)

// TestRefresherReusesConnectionAcrossPolls installs an http.Server.ConnState
// callback, which compiled net/http invokes with a net.Conn argument. That
// compiled-to-interpreted callback boundary deadlocks under Yaegi, so the test
// skips itself there; the regular `go test` job verifies connection reuse.
func TestRefresherReusesConnectionAcrossPolls(t *testing.T) {
	if runningUnderYaegi() {
		t.Skip("http.Server.ConnState callback deadlocks under Yaegi; covered by `go test`")
	}

	plugin.ResetSharedCacheForTesting()
	defer plugin.ResetSharedCacheForTesting()

	var conns int32
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Trailing newline left unread by a naive json.Decode — exercises the drain.
		_, _ = io.WriteString(w, `{"system_config":{"maintenance":{"is_active":true,"whitelist":["1.2.3.4"]}}}`+"\n")
	}))
	// ConnState must be set before Start to avoid racing the server goroutine.
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			atomic.AddInt32(&conns, 1)
		}
	}
	ts.Start()
	defer ts.Close()

	cfg := plugin.CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL + "/"}
	cfg.CacheDurationInSeconds = 1
	if _, err := plugin.New(context.Background(), nextNoop(), cfg, "test"); err != nil {
		t.Fatalf("New: %v", err)
	}
	// Let the background refresher poll a few times.
	time.Sleep(2500 * time.Millisecond)

	if got := atomic.LoadInt32(&conns); got > 2 {
		t.Fatalf("expected connection reuse (<=2 new conns), got %d — body not drained before Close", got)
	}
}

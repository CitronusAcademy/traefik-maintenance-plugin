package maintenance

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWarmupIsSynchronousForAllEnvironments(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":true,"whitelist":["*"]}}}`))
	}))
	defer ts.Close()

	endpoints := map[string]string{".a": ts.URL, ".b": ts.URL, ".c": ts.URL, "": ts.URL}
	Init(endpoints, nil, "", "", 60*time.Second, 5*time.Second, false, "ua")

	// By the time Init returns, EVERY environment must already be cached active —
	// not just one. No sleeps: this asserts synchronous warmup.
	for _, host := range []string{"x.a", "x.b", "x.c", "x.other"} {
		if active, _ := StatusForDomain(host); !active {
			t.Fatalf("environment for host %q not warmed synchronously", host)
		}
	}
}

// A closed stopCh must make warmupEnvironment return promptly even when every
// fetch fails (no cache initialized → refreshMaintenanceStatusForEnvironment
// returns false), instead of sleeping through the full backoff schedule.
func TestWarmupEnvironmentStopsOnClosedChannel(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	stop := make(chan struct{})
	close(stop)

	done := make(chan struct{})
	go func() {
		warmupEnvironment(".never", false, stop)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("warmupEnvironment did not honor closed stopCh; it slept through backoff")
	}
}

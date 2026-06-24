package traefik_maintenance_plugin

import (
	"testing"
	"time"
)

// A closed stopCh must make warmupEnvironment return promptly even when every
// fetch fails (no cache initialized → refreshMaintenanceStatusForEnvironment
// returns false), instead of sleeping through the full backoff schedule.
func TestWarmupEnvironmentStopsOnClosedChannel(t *testing.T) {
	ResetSharedCacheForTesting()
	defer ResetSharedCacheForTesting()

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

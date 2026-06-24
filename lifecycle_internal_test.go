package traefik_maintenance_plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloseSharedCacheRaceAgainstReaders(t *testing.T) {
	ResetSharedCacheForTesting()
	defer ResetSharedCacheForTesting()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":true,"whitelist":["*"]}}}`))
	}))
	defer ts.Close()

	cfg := CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL}
	if _, err := New(context.Background(), nopHandler(), cfg, "test"); err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			getMaintenanceStatusForDomain("host.example.com") // reads sharedCache.initialized under RLock
		}
		close(done)
	}()
	CloseSharedCache() // writes initialized/client — must be under the same lock
	<-done
}

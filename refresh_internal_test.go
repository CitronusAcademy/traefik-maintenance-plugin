package traefik_maintenance_plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshRejectsEmptyBodyOn200(t *testing.T) {
	ResetSharedCacheForTesting()
	defer ResetSharedCacheForTesting()

	// API returns 200 but an empty JSON object — no maintenance state.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	cfg := CreateConfig()
	cfg.EnvironmentEndpoints = map[string]string{"": ts.URL}
	cfg.CacheDurationInSeconds = 60
	if _, err := New(context.Background(), nopHandler(), cfg, "test"); err != nil {
		t.Fatalf("New: %v", err)
	}

	// A 200 with no maintenance object must NOT be cached as success.
	// The env should have no active state and should be eligible to retry (failed fetch).
	active, _ := getMaintenanceStatusForDomain("anything")
	if active {
		t.Fatalf("empty-body 200 should not mark maintenance active")
	}
	// And it must have recorded a failure, not a success: a success would set a
	// 60s expiry and suppress retries. Assert the entry is in the failed state.
	sharedCache.RLock()
	ec := sharedCache.environments[""]
	sharedCache.RUnlock()
	if ec == nil || ec.failedAttempts == 0 {
		t.Fatalf("empty-body 200 should be recorded as a failed fetch, got %+v", ec)
	}
}

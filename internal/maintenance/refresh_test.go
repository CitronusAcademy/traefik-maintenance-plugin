package maintenance

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefreshRejectsEmptyBodyOn200(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	// API returns 200 but an empty JSON object — no maintenance state.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	Init(map[string]string{"": ts.URL}, nil, "", "", 60*time.Second, 5*time.Second, false, "test")

	// A 200 with no maintenance object must NOT be cached as success.
	// The env should have no active state and should be eligible to retry (failed fetch).
	active, _ := StatusForDomain("anything")
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

func TestCalculateBackoff(t *testing.T) {
	if d := calculateBackoff(0); d != 5*time.Second {
		t.Errorf("attempts=0: got %v, want 5s", d)
	}
	if d := calculateBackoff(-3); d != 5*time.Second {
		t.Errorf("negative attempts: got %v, want 5s", d)
	}
	// attempts=1 → base 10s ± 20% jitter.
	if d := calculateBackoff(1); d < 8*time.Second || d > 12*time.Second {
		t.Errorf("attempts=1: got %v, want within [8s,12s]", d)
	}
	// Large attempts are capped at 1h.
	if d := calculateBackoff(50); d != time.Hour {
		t.Errorf("attempts=50: got %v, want 1h cap", d)
	}
}

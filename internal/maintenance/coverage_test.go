package maintenance

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshAllEnvironments_RefreshesEverySuffix(t *testing.T) {
	ResetForTesting()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":false,"whitelist":[]}}}`))
	}))
	defer srv.Close()
	// A long cache duration keeps the background ticker dormant after the
	// synchronous startup warmup, so the only refresh in flight is the explicit
	// one below. Force every env's cache to expire first so the call re-fetches.
	Init(map[string]string{"": srv.URL, ".pro": srv.URL, ".world": srv.URL},
		map[string]Secret{}, "", "", time.Hour, 5*time.Second, true, "test")
	defer Close()

	sharedCache.Lock()
	for _, env := range sharedCache.environments {
		env.expiry = time.Now().Add(-time.Hour)
	}
	sharedCache.Unlock()

	before := atomic.LoadInt32(&hits)
	refreshAllEnvironments()
	if atomic.LoadInt32(&hits)-before < 3 {
		t.Fatalf("refreshAllEnvironments hit %d endpoints, want >=3", atomic.LoadInt32(&hits)-before)
	}
}

func TestFetchMaintenanceState_ErrorPaths(t *testing.T) {
	client := &http.Client{}
	for _, debug := range []bool{false, true} {
		s500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		if _, ok := fetchMaintenanceState(client, s500.URL, "ua", "", "", time.Second, "", debug); ok {
			t.Fatalf("non-200 should be ok=false (debug=%v)", debug)
		}
		s500.Close()

		sBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{not json"))
		}))
		if _, ok := fetchMaintenanceState(client, sBad.URL, "ua", "", "", time.Second, "", debug); ok {
			t.Fatalf("invalid JSON should be ok=false (debug=%v)", debug)
		}
		sBad.Close()

		sClosed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := sClosed.URL
		sClosed.Close() // force a connection error
		if _, ok := fetchMaintenanceState(client, url, "ua", "secret-h", "secret-v", time.Second, ".pro", debug); ok {
			t.Fatalf("connection error should be ok=false (debug=%v)", debug)
		}
	}
}

func TestInit_InvalidDurationsDefaultedWithDebug(t *testing.T) {
	ResetForTesting()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":false,"whitelist":[]}}}`))
	}))
	defer srv.Close()
	// Zero durations + debug exercise the "invalid duration, using default" log
	// branches in Init and the surrounding warmup/warn debug paths.
	Init(map[string]string{"": srv.URL}, map[string]Secret{}, "X-Plugin-Secret", "tok", 0, 0, true, "test")
	defer Close()
	if active, _ := StatusForDomain("example.com"); active {
		t.Fatal("expected inactive maintenance state")
	}
	// A domain with no matching environment exercises the not-found branch.
	_, _ = StatusForDomain("nonexistent.invalid")
}

func TestFetchMaintenanceState_NoMaintenanceObject(t *testing.T) {
	client := &http.Client{}
	for _, debug := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`)) // 200 but no system_config/maintenance
		}))
		if _, ok := fetchMaintenanceState(client, srv.URL, "ua", "", "", time.Second, "", debug); ok {
			t.Fatalf("200 without maintenance object should be ok=false (debug=%v)", debug)
		}
		srv.Close()
	}
}

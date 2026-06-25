package maintenance

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCloseRaceAgainstReaders(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"system_config":{"maintenance":{"is_active":true,"whitelist":["*"]}}}`))
	}))
	defer ts.Close()

	Init(map[string]string{"": ts.URL}, nil, "", "", 10*time.Second, 5*time.Second, false, "test")

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			StatusForDomain("host.example.com") // reads sharedCache.initialized under RLock
		}
		close(done)
	}()
	Close() // writes initialized/client — must be under the same lock
	<-done
}

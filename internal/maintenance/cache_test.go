package maintenance

import (
	"strings"
	"testing"
	"time"
)

func TestStartupWarnsOnMissingSecret(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	out := captureStdout(t, func() {
		// No per-env secret value, no top-level secret value → unprotected env.
		Init(
			map[string]string{"": "http://unused.invalid/"},
			map[string]Secret{"": {Header: "X-Plugin-Secret", Value: ""}},
			"", "",
			60*time.Second, 5*time.Second, false, "ua",
		)
	})
	if !strings.Contains(out, "no secret") {
		t.Fatalf("expected a missing-secret warning, got: %q", out)
	}
}

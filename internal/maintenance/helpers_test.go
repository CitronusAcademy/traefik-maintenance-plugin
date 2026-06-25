package maintenance

import (
	"bytes"
	"testing"

	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/logx"
)

// captureStdout redirects the package log writer for the duration of fn and
// returns whatever fn wrote there. Test-only; mutates the package-global
// logx.Out, so callers must not run in parallel. Swapping logx.Out (not
// os.Stdout) keeps this runnable under Yaegi.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := logx.Out
	var buf bytes.Buffer
	logx.Out = &buf
	defer func() { logx.Out = orig }()
	fn()
	return buf.String()
}

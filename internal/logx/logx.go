// Package logx holds the plugin's diagnostic output writer. Routing all
// diagnostics through a single swappable writer (instead of os.Stdout)
// centralizes output and keeps capture working under Yaegi.
package logx

import (
	"io"
	"os"
)

// Out is the destination for diagnostic/debug output (os.Stdout by default;
// tests swap it for a buffer).
//
// Out is declared with no initializer and assigned os.Stdout in init so that
// its static type stays io.Writer. Yaegi (Traefik's interpreter) infers an
// imported package var's type from its initializer expression, so the obvious
// `var Out io.Writer = os.Stdout` would bind Out as the concrete *os.File and
// reject a buffer swap in tests; an interface-typed declaration plus an in-init
// assignment keeps the swap working under the interpreter.
var Out io.Writer

func init() {
	Out = os.Stdout
}

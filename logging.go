package traefik_maintenance_plugin

import (
	"io"
	"os"
)

// logOut is the destination for the plugin's diagnostic/debug output. It
// defaults to os.Stdout in production; tests swap it for a buffer. Routing
// diagnostics through a package writer (instead of writing to os.Stdout
// directly) keeps output capture working under Traefik's Yaegi interpreter,
// which cannot assign to the compiled os.Stdout variable.
var logOut io.Writer = os.Stdout

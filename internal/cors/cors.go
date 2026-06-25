// Package cors writes CORS response headers for the maintenance middleware. The
// preflight/blocked-response orchestration that decides WHEN to write them lives
// in the root package (it needs maintenance status, the whitelist, and the
// configured status code); this package only owns the header-writing policy.
package cors

import (
	"fmt"
	"net/http"

	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/logx"
)

// WriteHeaders writes the CORS allow headers for origin when it is permitted by
// allowedOrigins (or by allowAny when the list is empty). A disallowed or empty
// origin is a no-op.
func WriteHeaders(rw http.ResponseWriter, origin string, allowedOrigins []string, allowAny, debug bool) {
	if origin == "" || !originAllowed(origin, allowedOrigins, allowAny) {
		return
	}

	rw.Header().Set("Access-Control-Allow-Origin", origin)
	rw.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	rw.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-CSRF-Token")
	rw.Header().Set("Access-Control-Allow-Credentials", "true")
	rw.Header().Set("Access-Control-Max-Age", "86400")

	if debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Wrote CORS headers for origin: %s\n", origin)
	}
}

// originAllowed reports whether origin may be reflected. With an empty
// allowedOrigins list the decision falls to allowAny (reflect any origin by
// default, or none when locked down); otherwise origin must be listed.
func originAllowed(origin string, allowedOrigins []string, allowAny bool) bool {
	if len(allowedOrigins) == 0 {
		return allowAny // empty list: reflect any (default) or none (opt-in lock-down)
	}
	for _, o := range allowedOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

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
//
// Access-Control-Allow-Credentials is sent only for an origin that matches an
// explicit allowedOrigins entry. An origin reflected by the allow-any wildcard
// does NOT receive credentials: reflecting an arbitrary Origin together with
// Allow-Credentials: true is the insecure CORS combination and would let any
// site make credentialed cross-origin requests.
func WriteHeaders(rw http.ResponseWriter, origin string, allowedOrigins []string, allowAny, debug bool) {
	allowed, explicit := originDecision(origin, allowedOrigins, allowAny)
	if origin == "" || !allowed {
		return
	}

	rw.Header().Set("Access-Control-Allow-Origin", origin)
	rw.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	rw.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-CSRF-Token")
	if explicit {
		rw.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	rw.Header().Set("Access-Control-Max-Age", "86400")

	if debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Wrote CORS headers for origin: %s\n", origin)
	}
}

// originDecision reports whether origin may be reflected (allowed) and whether
// it matched an explicit allowedOrigins entry (explicit). With an empty
// allowedOrigins list the decision falls to allowAny (reflect any origin by
// default, or none when locked down) and is never explicit; otherwise origin
// must be listed, in which case the match is explicit.
func originDecision(origin string, allowedOrigins []string, allowAny bool) (allowed, explicit bool) {
	if len(allowedOrigins) == 0 {
		return allowAny, false // empty list: reflect any (default) or none — never explicit
	}
	for _, o := range allowedOrigins {
		if o == origin {
			return true, true
		}
	}
	return false, false
}

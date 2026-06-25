package cors

import (
	"net/http/httptest"
	"testing"
)

func TestOriginDecision(t *testing.T) {
	if allowed, _ := originDecision("https://anything.example", nil, false); allowed {
		t.Fatal("with allowAny=false and an empty allow-list, no origin should be allowed")
	}
	// Back-compat default still reflects any origin, but never as an explicit match.
	if allowed, explicit := originDecision("https://anything.example", nil, true); !allowed || explicit {
		t.Fatalf("allowAny must reflect (allowed) but not be explicit: allowed=%v explicit=%v", allowed, explicit)
	}
	if allowed, explicit := originDecision("https://ok.example", []string{"https://ok.example"}, false); !allowed || !explicit {
		t.Fatalf("listed origin must be allowed and explicit: allowed=%v explicit=%v", allowed, explicit)
	}
	if allowed, _ := originDecision("https://no.example", []string{"https://ok.example"}, false); allowed {
		t.Fatal("unlisted origin must be denied")
	}
}

func TestWriteHeaders(t *testing.T) {
	// Allow-any reflected origin (debug on): headers written, but NO credentials —
	// reflecting an arbitrary origin with credentials is the insecure combination.
	rw := httptest.NewRecorder()
	WriteHeaders(rw, "https://app.example", nil, true, true)
	if got := rw.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("expected origin reflected, got %q", got)
	}
	if got := rw.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("wildcard-reflected origin must not get credentials, got %q", got)
	}

	// Explicitly allow-listed origin: credentials ARE sent.
	rwc := httptest.NewRecorder()
	WriteHeaders(rwc, "https://ok.example", []string{"https://ok.example"}, false, false)
	if got := rwc.Header().Get("Access-Control-Allow-Origin"); got != "https://ok.example" {
		t.Fatalf("expected allow-listed origin reflected, got %q", got)
	}
	if got := rwc.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow-listed origin must get credentials, got %q", got)
	}

	// Empty origin → no-op.
	rw2 := httptest.NewRecorder()
	WriteHeaders(rw2, "", nil, true, false)
	if rw2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("empty origin must not write CORS headers")
	}

	// Disallowed origin → no-op.
	rw3 := httptest.NewRecorder()
	WriteHeaders(rw3, "https://evil.example", []string{"https://ok.example"}, false, false)
	if rw3.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disallowed origin must not write CORS headers")
	}
}

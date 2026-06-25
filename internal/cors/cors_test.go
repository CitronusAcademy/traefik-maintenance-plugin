package cors

import (
	"net/http/httptest"
	"testing"
)

func TestOriginAllowed(t *testing.T) {
	if originAllowed("https://anything.example", nil, false) {
		t.Fatal("with allowAny=false and an empty allow-list, no origin should be allowed")
	}
	// Back-compat default still reflects any origin.
	if !originAllowed("https://anything.example", nil, true) {
		t.Fatal("default allowAny=true must reflect any origin")
	}
	if !originAllowed("https://ok.example", []string{"https://ok.example"}, false) {
		t.Fatal("listed origin must be allowed")
	}
	if originAllowed("https://no.example", []string{"https://ok.example"}, false) {
		t.Fatal("unlisted origin must be denied")
	}
}

func TestWriteHeaders(t *testing.T) {
	// Allowed (allowAny) origin with debug on → headers written.
	rw := httptest.NewRecorder()
	WriteHeaders(rw, "https://app.example", nil, true, true)
	if got := rw.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("expected origin reflected, got %q", got)
	}
	if rw.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("expected credentials header")
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

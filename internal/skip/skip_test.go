package skip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStrictAssetMatching(t *testing.T) {
	exts := []string{".css", ".js"}

	r := func(p string) *http.Request { return httptest.NewRequest(http.MethodGet, p, nil) }

	// Lenient (default): a crafted trailing segment matches.
	if !IsStaticAsset(r("/api/export.css"), exts, false) {
		t.Fatal("lenient mode should match a path ending in .css")
	}
	// Strict: only a real last-segment file extension matches a true asset...
	if !IsStaticAsset(r("/static/app.min.css"), exts, true) {
		t.Fatal("strict mode should match a genuine .css asset")
	}
	// ...and rejects a path whose last segment is not a bare file.ext asset.
	if IsStaticAsset(r("/data.json/x"), exts, true) {
		t.Fatal("strict mode should not match /data.json/x")
	}
}

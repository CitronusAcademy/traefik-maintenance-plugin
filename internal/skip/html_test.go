package skip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsHTMLRequest(t *testing.T) {
	if IsHTMLRequest(nil) {
		t.Error("nil request must not be HTML")
	}

	post := httptest.NewRequest(http.MethodPost, "/", nil)
	post.Header.Set("Accept", "text/html")
	if IsHTMLRequest(post) {
		t.Error("POST must not be treated as HTML")
	}

	bare := httptest.NewRequest(http.MethodGet, "/", nil)
	if IsHTMLRequest(bare) {
		t.Error("GET without Accept must not be HTML")
	}

	htmlReq := httptest.NewRequest(http.MethodGet, "/", nil)
	htmlReq.Header.Set("Accept", "application/json, text/html;q=0.9")
	if !IsHTMLRequest(htmlReq) {
		t.Error("GET accepting text/html must be HTML")
	}

	head := httptest.NewRequest(http.MethodHead, "/", nil)
	head.Header.Set("Accept", "text/html")
	if !IsHTMLRequest(head) {
		t.Error("HEAD accepting text/html must be HTML")
	}

	jsonReq := httptest.NewRequest(http.MethodGet, "/", nil)
	jsonReq.Header.Set("Accept", "application/json")
	if IsHTMLRequest(jsonReq) {
		t.Error("GET accepting only json must not be HTML")
	}
}

func TestIsStaticAssetGuards(t *testing.T) {
	exts := []string{".css"}

	if IsStaticAsset(nil, exts, false) {
		t.Error("nil request must not be a static asset")
	}

	post := httptest.NewRequest(http.MethodPost, "/a.css", nil)
	if IsStaticAsset(post, exts, false) {
		t.Error("POST must not be a static asset")
	}

	get := httptest.NewRequest(http.MethodGet, "/a.css", nil)
	if IsStaticAsset(get, nil, false) {
		t.Error("no configured extensions must not match")
	}

	noExt := httptest.NewRequest(http.MethodGet, "/dir/", nil)
	if IsStaticAsset(noExt, exts, true) {
		t.Error("strict mode with no real extension must not match")
	}

	other := httptest.NewRequest(http.MethodGet, "/a.png", nil)
	if IsStaticAsset(other, exts, true) {
		t.Error("strict mode with a non-listed extension must not match")
	}
}

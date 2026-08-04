package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandler(t *testing.T) {
	t.Parallel()

	assets := fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<main>app</main>"), Mode: fs.ModePerm},
		"main-ABC12345.js":     &fstest.MapFile{Data: []byte("console.log('app')"), Mode: fs.ModePerm},
		"manifest.webmanifest": &fstest.MapFile{Data: []byte("{}"), Mode: fs.ModePerm},
	}
	handler := newSPAHandler(assets)

	tests := []struct {
		name         string
		path         string
		status       int
		body         string
		cacheControl string
	}{
		{name: "root", path: "/", status: http.StatusOK, body: "<main>app</main>", cacheControl: "no-cache"},
		{name: "route", path: "/settings/providers", status: http.StatusOK, body: "<main>app</main>", cacheControl: "no-cache"},
		{name: "fingerprinted asset", path: "/main-ABC12345.js", status: http.StatusOK, body: "console.log('app')", cacheControl: "public, max-age=31536000, immutable"},
		{name: "stable asset", path: "/manifest.webmanifest", status: http.StatusOK, body: "{}", cacheControl: "public, max-age=3600"},
		{name: "missing asset", path: "/missing.js", status: http.StatusNotFound, body: "404 page not found\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))

			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if body := response.Body.String(); body != test.body {
				t.Fatalf("body = %q, want %q", body, test.body)
			}
			if cacheControl := response.Header().Get("Cache-Control"); cacheControl != test.cacheControl {
				t.Fatalf("Cache-Control = %q, want %q", cacheControl, test.cacheControl)
			}
		})
	}
}

package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateListenAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		address     string
		allowRemote bool
		wantError   bool
	}{
		{name: "IPv4 loopback", address: "127.0.0.1:9000"},
		{name: "IPv6 loopback", address: "[::1]:9000"},
		{name: "localhost", address: "localhost:9000"},
		{name: "remote requires opt-in", address: "192.0.2.10:9000", wantError: true},
		{name: "wildcard requires opt-in", address: ":9000", wantError: true},
		{name: "remote explicitly allowed", address: "0.0.0.0:9000", allowRemote: true},
		{name: "invalid", address: "localhost", allowRemote: true, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateListenAddress(test.address, test.allowRemote)
			if (err != nil) != test.wantError {
				t.Fatalf("ValidateListenAddress(%q, %t) error = %v, wantError %t", test.address, test.allowRemote, err, test.wantError)
			}
		})
	}
}

func TestResolvePublicOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		address    string
		configured string
		want       string
		wantError  bool
	}{
		{name: "derived IPv4", address: "127.0.0.1:9000", want: "http://127.0.0.1:9000"},
		{name: "derived wildcard", address: ":9000", want: "http://127.0.0.1:9000"},
		{name: "configured", address: "127.0.0.1:9000", configured: " https://tasks.example.com/ ", want: "https://tasks.example.com"},
		{name: "reject path", address: "127.0.0.1:9000", configured: "https://example.com/app", wantError: true},
		{name: "reject non HTTP", address: "127.0.0.1:9000", configured: "file:///tmp/app", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolvePublicOrigin(test.address, test.configured)
			if (err != nil) != test.wantError {
				t.Fatalf("ResolvePublicOrigin() error = %v, wantError %t", err, test.wantError)
			}
			if got != test.want {
				t.Fatalf("ResolvePublicOrigin() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMiddleware(t *testing.T) {
	t.Parallel()

	handler, err := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "https://app.example.com")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("adds security headers", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9000/", nil))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
		}
		for _, name := range []string{
			"Content-Security-Policy",
			"Cross-Origin-Resource-Policy",
			"Permissions-Policy",
			"Referrer-Policy",
			"X-Content-Type-Options",
			"X-Frame-Options",
		} {
			if recorder.Header().Get(name) == "" {
				t.Errorf("missing %s header", name)
			}
		}
	})

	t.Run("rejects cross-site mutation", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/api/action", nil)
		request.Header.Set("Sec-Fetch-Site", "cross-site")
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
	})

	t.Run("allows same-origin mutation", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/api/action", nil)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
		}
	})

	t.Run("allows trusted origin", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/api/action", nil)
		request.Header.Set("Origin", "https://app.example.com")
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
		}
	})
}

func TestMiddlewareRecoversPanics(t *testing.T) {
	t.Parallel()

	handler, err := Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("test panic")
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9000/api/test", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(recorder.Body.String(), "internal server error") {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

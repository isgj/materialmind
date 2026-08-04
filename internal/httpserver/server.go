package httpserver

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const contentSecurityPolicy = "default-src 'self'; base-uri 'self'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data: blob:; object-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'"

// ValidateListenAddress requires an explicit opt-in before exposing the app to
// other machines. These applications can access local files and processes.
func ValidateListenAddress(address string, allowRemote bool) error {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("parse HTTP listen address: %w", err)
	}
	if allowRemote || IsLoopbackAddress(address) {
		return nil
	}
	return fmt.Errorf("refusing non-loopback HTTP address %q without -allow-remote", address)
}

func IsLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ResolvePublicOrigin validates an explicitly configured browser origin or
// derives the local origin used by the default direct-listener setup.
func ResolvePublicOrigin(address, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return "", fmt.Errorf("parse HTTP listen address: %w", err)
		}
		switch host {
		case "", "0.0.0.0", "::":
			host = "127.0.0.1"
		}
		configured = "http://" + net.JoinHostPort(host, port)
	}

	parsed, err := url.Parse(configured)
	if err != nil || parsed.Hostname() == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("public URL must be an HTTP(S) origin")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

// Middleware applies the common browser and response security policy at the
// outermost HTTP boundary so API and frontend handlers behave consistently.
func Middleware(next http.Handler, trustedOrigin string) (http.Handler, error) {
	crossOrigin := http.NewCrossOriginProtection()
	if trustedOrigin != "" {
		if err := crossOrigin.AddTrustedOrigin(trustedOrigin); err != nil {
			return nil, fmt.Errorf("trust public origin: %w", err)
		}
	}
	crossOrigin.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "cross-origin browser request rejected", http.StatusForbidden)
	}))
	protected := crossOrigin.Handler(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(r.Context(), "panic while serving request", "method", r.Method, "path", r.URL.Path, "panic", recovered)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		protected.ServeHTTP(w, r)
	}), nil
}

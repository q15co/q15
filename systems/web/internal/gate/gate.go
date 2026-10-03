// Package gate owns the replaceable browser authorization seam and HTTP policy.
package gate

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Principal is provided by authorization, never by a browser payload.
type Principal struct{ ID string }

type principalKey struct{}

// WithPrincipal is the authorization seam's identity hand-off to handlers.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// PrincipalFrom returns the authorized request's principal.
func PrincipalFrom(ctx context.Context) Principal {
	value, _ := ctx.Value(principalKey{}).(Principal)
	return value
}

// Authorizer is the middleware seam replaced by the later authentication slice.
type Authorizer interface {
	RequireScope(string, http.Handler) http.Handler
}

// TemporaryToken grants only the single owner's chat scope.
type TemporaryToken struct{ token string }

// NewTemporaryToken refuses an empty gate rather than starting an open server.
func NewTemporaryToken(token string) (*TemporaryToken, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("Q15_WEB_TOKEN is required")
	}
	return &TemporaryToken{token: token}, nil
}

// RequireScope accepts a bearer token or browser-native Basic auth (q15/token).
// No query parameter, WebSocket subprotocol or JS-readable cookie holds a token.
func (g *TemporaryToken) RequireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == r.Header.Get("Authorization") {
			token = ""
		}
		if username, password, ok := r.BasicAuth(); ok && username == "q15" {
			token = password
		}
		// This temporary gate compares the environment token itself; it is not
		// a password database or a password-hashing scheme. Equal-length tokens
		// are compared in constant time without revealing matching prefixes.
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(g.token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="q15-web", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if scope != "chat" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r = r.WithContext(WithPrincipal(r.Context(), Principal{ID: "owner"}))
		next.ServeHTTP(w, r)
	})
}

// ValidateOrigin validates an exact origin; Host and forwarded headers cannot
// redefine the origin which is allowed to exercise credentials.
func ValidateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(origin, "\r\n") {
		return fmt.Errorf("Q15_WEB_ORIGIN must be an http(s) origin without a path")
	}
	return nil
}

// CheckOrigin requires exactly the configured origin. Fetch metadata adds a
// second check when supplied; native browser WebSocket handshakes may omit it.
func CheckOrigin(r *http.Request, origin string) bool {
	values := r.Header.Values("Origin")
	if len(values) != 1 || values[0] != origin {
		return false
	}
	sites := r.Header.Values("Sec-Fetch-Site")
	return len(sites) == 0 || (len(sites) == 1 && sites[0] == "same-origin")
}

// Headers applies policy to successes, failures, assets and upgrades alike.
func Headers(origin string, next http.Handler) http.Handler {
	wsOrigin := "ws" + strings.TrimPrefix(origin, "http")
	csp := "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self' " + wsOrigin + "; base-uri 'none'; frame-ancestors 'none'; object-src 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

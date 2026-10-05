// Package gate owns the replaceable browser authorization seam and HTTP policy.
package gate

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Principal is provided by authorization, never by a browser payload.
type Principal struct {
	ID      string
	Binding string
}

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

// Authorizer supplies identity and scope before a handler can reach the bridge.
type Authorizer interface {
	RequireScope(string, http.Handler) http.Handler
}

// SessionChecker revalidates authorization after a WebSocket upgrade.
type SessionChecker interface {
	SessionValid(context.Context) bool
}

// ServeShell uses the same compiled UI for locked and authenticated navigation.
// Inlining its bootstrap allows a 401 response without exposing static routes.
func ServeShell(w http.ResponseWriter, shell []byte, status int) {
	nonce := rand.Text()
	csp := w.Header().Get("Content-Security-Policy")
	csp = strings.Replace(csp, "script-src 'self'", "script-src 'nonce-"+nonce+"'", 1)
	csp = strings.Replace(csp, "style-src 'self'", "style-src 'nonce-"+nonce+"'", 1)
	csp = strings.Replace(csp, "font-src 'self'", "font-src 'self' data:", 1)
	csp += "; worker-src 'self'"
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(strings.ReplaceAll(string(shell), "__Q15_NONCE__", nonce)))
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

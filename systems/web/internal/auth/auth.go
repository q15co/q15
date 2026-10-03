package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/q15co/q15/systems/web/internal/gate"
)

const (
	sessionCookie    = "__Host-q15s"
	challengeCookie  = "__Host-q15c"
	ceremonyLifetime = 2 * time.Minute
	maxCeremonies    = 64
	maxResponseBytes = 64 << 10
)

type ceremony struct {
	Data      webauthn.SessionData
	Expires   time.Time
	Name      string
	PublicKey []byte
}

// Authenticator owns credentials, hashed sessions and short-lived ceremonies.
type Authenticator struct {
	shell      []byte
	mu         sync.Mutex
	directory  string
	origin     string
	webAuthn   *webauthn.WebAuthn
	lock       *os.File
	state      state
	failed     bool
	login      map[string]ceremony
	enrollment map[string]ceremony
	now        func() time.Time
	logger     *slog.Logger
	rateStart  time.Time
	rateCount  int
}

// Open requires HTTPS (or localhost development) and exclusive private storage.
func Open(directory, origin string, shell []byte, logger *slog.Logger) (*Authenticator, error) {
	if !bytes.Contains(shell, []byte("__Q15_NONCE__")) {
		return nil, errors.New("compiled UI shell is required")
	}
	if err := gate.ValidateOrigin(origin); err != nil {
		return nil, err
	}
	u, _ := url.Parse(origin)
	if u.Scheme != "https" && u.Hostname() != "localhost" {
		return nil, errors.New(
			"owner authentication requires HTTPS, except http://localhost development",
		)
	}
	wa, err := webauthn.New(
		&webauthn.Config{RPDisplayName: "q15", RPID: u.Hostname(), RPOrigins: []string{origin},
			AuthenticatorSelection: protocol.AuthenticatorSelection{
				UserVerification: protocol.VerificationRequired,
			},
			AttestationPreference: protocol.PreferNoAttestation},
	)
	if err != nil {
		return nil, err
	}
	lock, err := openStore(directory)
	if err != nil {
		return nil, err
	}
	s, err := loadState(filepath.Join(directory, "auth.json"), origin)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	a := &Authenticator{
		shell:      shell,
		directory:  directory,
		origin:     origin,
		webAuthn:   wa,
		lock:       lock,
		state:      s,
		login:      make(map[string]ceremony),
		enrollment: make(map[string]ceremony),
		now:        time.Now,
		logger:     logger,
	}
	if err := a.commit(a.nextState()); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return a, nil
}

// Close releases the store lock after all HTTP and admin work has stopped.
func (a *Authenticator) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failed = true
	return a.lock.Close()
}

func randomBytes(size int) []byte {
	value := make([]byte, size)
	_, _ = rand.Read(value)
	return value
}
func credentialID(id []byte) string { return base64.RawURLEncoding.EncodeToString(id) }
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func newToken() string { return credentialID(randomBytes(32)) }

func cookie(w http.ResponseWriter, name, value string, lifetime time.Duration) {
	maxAge := int(lifetime.Seconds())
	if lifetime < 0 {
		maxAge = -1
	}
	http.SetCookie(
		w,
		&http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true,
			SameSite: http.SameSiteStrictMode, MaxAge: maxAge},
	)
}

func requestToken(r *http.Request, name string) string {
	var value string
	found := false
	for _, c := range r.Cookies() {
		if c.Name == name {
			if found {
				return ""
			}
			found = true
			value = c.Value
		}
	}
	if len(value) != 43 {
		return ""
	}
	return value
}

type sessionKey struct{}

func (a *Authenticator) valid(key string) bool {
	s, ok := a.state.Sessions[key]
	_, enrolled := a.state.Devices[s.Device]
	return !a.failed && ok && enrolled && s.Scope == "chat" && a.now().Before(s.Expires)
}

// SessionValid checks live state for socket reads, writes and idle expiry.
func (a *Authenticator) SessionValid(ctx context.Context) bool {
	key, _ := ctx.Value(sessionKey{}).(string)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.valid(key)
}

// RequireScope allows only the WebAuthn proof exchange before a session exists.
// An unauthenticated challenge still answers 401 and exposes no application data.
func (a *Authenticator) RequireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if scope == "chat" && r.Method == http.MethodPost &&
			(r.URL.Path == "/auth/login" || r.URL.Path == "/auth/login/finish") {
			a.signIn(w, r)
			return
		}
		key := digest(requestToken(r, sessionCookie))
		a.mu.Lock()
		valid := a.valid(key) && a.verifyProof(key, r)
		a.mu.Unlock()
		if !valid {
			a.unauthorized(w, r)
			return
		}
		if scope != "chat" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			!mutationOrigin(r, a.origin) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/auth/worker" && r.Method == http.MethodPost {
			a.workerBootstrap(w, r)
			return
		}
		if r.URL.Path == "/sw.js" {
			cookie(w, workerCookie, "", -1)
		}
		ctx := context.WithValue(r.Context(), sessionKey{}, key)
		r = r.WithContext(gate.WithPrincipal(ctx, gate.Principal{ID: "owner"}))
		if r.URL.Path == "/auth/session" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/auth/logout" && r.Method == http.MethodPost {
			a.logout(w, key)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mutationOrigin(r *http.Request, origin string) bool {
	return gate.CheckOrigin(r, origin) && len(r.Header.Values("Sec-Fetch-Site")) == 1 &&
		r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

func (a *Authenticator) allowLogin() bool {
	now := a.now()
	if now.Sub(a.rateStart) >= time.Minute {
		a.rateStart = now
		a.rateCount = 0
	}
	a.rateCount++
	return a.rateCount <= 120
}

func (a *Authenticator) pruneCeremonies() {
	for _, ceremonies := range []map[string]ceremony{a.login, a.enrollment} {
		for key, c := range ceremonies {
			if !a.now().Before(c.Expires) {
				delete(ceremonies, key)
			}
		}
	}
}

func (a *Authenticator) signIn(w http.ResponseWriter, r *http.Request) {
	if !mutationOrigin(r, a.origin) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failed {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	if !a.allowLogin() {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "try again later", http.StatusTooManyRequests)
		return
	}
	a.pruneCeremonies()
	if r.URL.Path == "/auth/login" {
		delete(a.login, digest(requestToken(r, challengeCookie)))
		if len(a.login) >= maxCeremonies {
			http.Error(w, "try again later", http.StatusTooManyRequests)
			return
		}
		var input struct {
			PublicKey string `json:"public_key"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "sign in refused", http.StatusUnauthorized)
			return
		}
		publicKey, err := parseSessionPublicKey(input.PublicKey)
		if err != nil {
			http.Error(w, "sign in refused", http.StatusUnauthorized)
			return
		}
		options, data, err := a.webAuthn.BeginDiscoverableLogin(
			webauthn.WithUserVerification(protocol.VerificationRequired),
		)
		if err != nil {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		token := newToken()
		a.login[digest(token)] = ceremony{
			Data:      *data,
			Expires:   a.now().Add(ceremonyLifetime),
			PublicKey: publicKey,
		}
		cookie(w, challengeCookie, token, ceremonyLifetime)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(options)
		return
	}
	key := digest(requestToken(r, challengeCookie))
	c, ok := a.login[key]
	delete(a.login, key)
	cookie(w, challengeCookie, "", -1)
	if !ok || !a.now().Before(c.Expires) {
		http.Error(w, "sign in again", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxResponseBytes)
	credential, err := a.webAuthn.FinishDiscoverableLogin(
		func(_, handle []byte) (webauthn.User, error) {
			if !bytes.Equal(handle, a.state.Owner) {
				return nil, errors.New("unknown owner")
			}
			return a.state.owner(), nil
		},
		c.Data,
		r,
	)
	if err != nil || credential.Flags.BackupEligible || credential.Authenticator.CloneWarning {
		a.logger.Warn("owner authentication", "event", "login_refused")
		http.Error(w, "sign in refused", http.StatusUnauthorized)
		return
	}
	next := a.nextState()
	id := credentialID(credential.ID)
	d, exists := next.Devices[id]
	if !exists || len(next.Sessions) >= maxSessions {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	d.Credential = *credential
	next.Devices[id] = d
	// Reauthentication replaces this browser's session instead of accumulating it.
	delete(next.Sessions, digest(requestToken(r, sessionCookie)))
	token := newToken()
	binding := newToken()
	next.Sessions[digest(token)] = session{
		Device:    id,
		Expires:   a.now().Add(sessionLifetime),
		Scope:     "chat",
		PublicKey: c.PublicKey,
		Binding:   binding,
		Used:      make(map[string]time.Time),
	}
	if err := a.commit(next); err != nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	cookie(w, sessionCookie, token, sessionLifetime)
	w.Header().Set("Q15-Session", binding)
	a.logger.Info("owner authentication", "event", "login", "device", id)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Authenticator) logout(w http.ResponseWriter, key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.nextState()
	id := a.state.Sessions[key].Device
	delete(next.Sessions, key)
	if err := a.commit(next); err != nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	cookie(w, sessionCookie, "", -1)
	a.logger.Info("owner authentication", "event", "logout", "device", id)
	w.WriteHeader(http.StatusNoContent)
}

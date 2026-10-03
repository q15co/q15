package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/q15co/q15/systems/web/internal/gate"
)

var testLoginPage = []byte(
	`<html><script nonce="__Q15_NONCE__">document.title="Sign in"</script></html>`,
)

const testOrigin = "https://chat.example"

func openTest(t *testing.T) *Authenticator {
	t.Helper()
	a, err := Open(
		filepath.Join(t.TempDir(), "auth"),
		testOrigin,
		testLoginPage,
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

type authenticator struct {
	id    []byte
	key   *ecdsa.PrivateKey
	count uint32
}

func newAuthenticator(t *testing.T) *authenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &authenticator{id: randomBytes(16), key: key}
}

func marshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func clientData(t *testing.T, kind, challenge, origin string) []byte {
	return marshal(
		t,
		map[string]any{
			"type":        kind,
			"challenge":   challenge,
			"origin":      origin,
			"crossOrigin": false,
		},
	)
}

func authenticatorData(flags byte, count uint32) []byte {
	hash := sha256.Sum256([]byte("chat.example"))
	data := append(hash[:], flags)
	return binary.BigEndian.AppendUint32(data, count)
}

func (d *authenticator) registration(
	t *testing.T,
	challenge, origin string,
	flags byte,
) json.RawMessage {
	t.Helper()
	key, err := cbor.Marshal(
		map[int]any{
			1:  2,
			3:  -7,
			-1: 1,
			-2: d.key.X.FillBytes(make([]byte, 32)),
			-3: d.key.Y.FillBytes(make([]byte, 32)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	authData := append(authenticatorData(flags, 0), make([]byte, 16)...)
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(d.id)))
	authData = append(append(authData, d.id...), key...)
	attestation, err := cbor.Marshal(
		map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData},
	)
	if err != nil {
		t.Fatal(err)
	}
	return marshal(
		t,
		map[string]any{
			"id":    credentialID(d.id),
			"rawId": credentialID(d.id),
			"type":  "public-key",
			"response": map[string]any{
				"clientDataJSON": credentialID(
					clientData(t, "webauthn.create", challenge, origin),
				),
				"attestationObject": credentialID(attestation),
			},
		},
	)
}

func enrollTest(t *testing.T, a *Authenticator, d *authenticator) string {
	t.Helper()
	options, err := a.beginEnrollment("device")
	if err != nil {
		t.Fatal(err)
	}
	challenge := a.enrollment[options.ID].Data.Challenge
	if err := a.finishEnrollment(enrollmentResponse{ID: options.ID, Response: d.registration(t, challenge, testOrigin, 0x45)}); err != nil {
		t.Fatal(err)
	}
	return credentialID(d.id)
}

func request(
	a *Authenticator,
	method, path string,
	body []byte,
	cookies ...*http.Cookie,
) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	gate.Headers(testOrigin, a.RequireScope("chat", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gate.PrincipalFrom(r.Context()).ID != "owner" {
			panic("missing owner")
		}
		w.WriteHeader(http.StatusNoContent)
	}))).
		ServeHTTP(w, r)
	return w
}

func findCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("missing cookie %s", name)
	return nil
}

func (d *authenticator) assertion(
	t *testing.T,
	challenge, origin string,
	owner []byte,
	flags byte,
) []byte {
	t.Helper()
	d.count++
	authData := authenticatorData(flags, d.count)
	client := clientData(t, "webauthn.get", challenge, origin)
	clientHash := sha256.Sum256(client)
	signed := sha256.Sum256(append(authData, clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, d.key, signed[:])
	if err != nil {
		t.Fatal(err)
	}
	return marshal(
		t,
		map[string]any{
			"id":    credentialID(d.id),
			"rawId": credentialID(d.id),
			"type":  "public-key",
			"response": map[string]any{
				"clientDataJSON":    credentialID(client),
				"authenticatorData": credentialID(authData),
				"signature":         credentialID(signature),
				"userHandle":        credentialID(owner),
			},
		},
	)
}

func loginTest(t *testing.T, a *Authenticator, d *authenticator) *http.Cookie {
	t.Helper()
	w := request(a, "POST", "/auth/login", []byte("{}"))
	if w.Code != 401 {
		t.Fatalf("begin status %d", w.Code)
	}
	cookie := findCookie(t, w, challengeCookie)
	c := a.login[digest(cookie.Value)]
	w = request(
		a,
		"POST",
		"/auth/login/finish",
		d.assertion(t, c.Data.Challenge, testOrigin, a.state.Owner, 0x05),
		cookie,
	)
	if w.Code != 204 {
		t.Fatalf("finish status %d: %s", w.Code, w.Body)
	}
	return findCookie(t, w, sessionCookie)
}

func TestOwnerLifecycleAndPersistence(t *testing.T) {
	a := openTest(t)
	first, second := newAuthenticator(t), newAuthenticator(t)
	firstID := enrollTest(t, a, first)
	enrollTest(t, a, second)
	firstSession, secondSession := loginTest(t, a, first), loginTest(t, a, second)
	if !firstSession.HttpOnly || !firstSession.Secure ||
		firstSession.SameSite != http.SameSiteStrictMode ||
		firstSession.Path != "/" ||
		firstSession.Domain != "" {
		t.Fatal("unsafe cookie")
	}
	data, err := os.ReadFile(filepath.Join(a.directory, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(firstSession.Value)) {
		t.Fatal("raw session on disk")
	}
	if request(a, "GET", "/api/turns", nil, firstSession).Code != 204 {
		t.Fatal("valid session denied")
	}
	if err := a.revoke(firstID); err != nil {
		t.Fatal(err)
	}
	if request(a, "GET", "/", nil, firstSession).Code != 401 ||
		request(a, "GET", "/", nil, secondSession).Code != 204 {
		t.Fatal("device revocation boundary")
	}
	if w := request(a, "POST", "/auth/logout", nil, secondSession); w.Code != 204 ||
		findCookie(t, w, sessionCookie).MaxAge != -1 {
		t.Fatal("logout failed")
	}
	if request(a, "GET", "/", nil, secondSession).Code != 401 {
		t.Fatal("logout session still valid")
	}
	secondSession = loginTest(t, a, second)
	loaded, err := loadState(filepath.Join(a.directory, "auth.json"), testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Sessions[digest(secondSession.Value)]; !ok {
		t.Fatal("session not persistent")
	}
	a.now = func() time.Time { return time.Now().Add(sessionLifetime) }
	if request(a, "GET", "/", nil, secondSession).Code != 401 {
		t.Fatal("expired session accepted")
	}
}

func TestPublicRouteAndMutationBoundary(t *testing.T) {
	a := openTest(t)
	for _, path := range []string{"/", "/ws", "/api/turns", "/auth/enroll", "/enroll/begin", "/devices", "/revoke", "/auth/logout", "/auth/session", "/assets/app.js", "/sw.js"} {
		for _, method := range []string{"GET", "POST", "HEAD"} {
			if w := request(a, method, path, nil); w.Code != 401 {
				t.Errorf("%s %s=%d", method, path, w.Code)
			}
		}
	}
	d := newAuthenticator(t)
	enrollTest(t, a, d)
	cookie := loginTest(t, a, d)
	for _, test := range []struct{ origin, site string }{{"", "same-origin"}, {testOrigin, ""}, {testOrigin, "same-site"}, {"https://evil.example", "same-origin"}} {
		for _, path := range []string{"/auth/login", "/auth/login/finish", "/auth/logout", "/api/mutation"} {
			r := httptest.NewRequest("POST", path, strings.NewReader("{}"))
			r.AddCookie(cookie)
			if test.origin != "" {
				r.Header.Set("Origin", test.origin)
			}
			if test.site != "" {
				r.Header.Set("Sec-Fetch-Site", test.site)
			}
			w := httptest.NewRecorder()
			a.RequireScope("chat", http.NotFoundHandler()).ServeHTTP(w, r)
			if w.Code != 403 {
				t.Errorf("%s %q %q=%d", path, test.origin, test.site, w.Code)
			}
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookie)
	a.RequireScope("console", http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("chat gained console")
	}
	for _, header := range []string{"Bearer old-token", "Basic cTE1OnNlY3JldA=="} {
		r := httptest.NewRequest("GET", "/?token=old-token", nil)
		r.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		a.RequireScope("chat", http.NotFoundHandler()).ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("legacy credential accepted")
		}
	}
}

func TestProofChecksAndSingleUse(t *testing.T) {
	for _, test := range []struct {
		name, origin        string
		flags               byte
		registered, expired bool
	}{
		{"wrong origin", "https://evil.example", 0x05, true, false}, {"no UV", testOrigin, 0x01, true, false}, {"unknown credential", testOrigin, 0x05, false, false}, {"expired challenge", testOrigin, 0x05, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := openTest(t)
			d := newAuthenticator(t)
			if test.registered {
				enrollTest(t, a, d)
			}
			w := request(a, "POST", "/auth/login", []byte("{}"))
			cookie := findCookie(t, w, challengeCookie)
			c := a.login[digest(cookie.Value)]
			body := d.assertion(t, c.Data.Challenge, test.origin, a.state.Owner, test.flags)
			if test.expired {
				a.now = func() time.Time { return time.Now().Add(3 * time.Minute) }
			}
			for range 2 {
				if request(
					a,
					"POST",
					"/auth/login/finish",
					body,
					cookie,
				).Code != http.StatusUnauthorized {
					t.Fatal("invalid or replayed proof accepted")
				}
			}
		})
	}
	a := openTest(t)
	d := newAuthenticator(t)
	enrollTest(t, a, d)
	w := request(a, "POST", "/auth/login", []byte("{}"))
	cookie := findCookie(t, w, challengeCookie)
	c := a.login[digest(cookie.Value)]
	body := d.assertion(t, c.Data.Challenge, testOrigin, a.state.Owner, 0x05)
	if request(a, "POST", "/auth/login/finish", body).Code != 401 {
		t.Fatal("unbound assertion accepted")
	}
	if request(a, "POST", "/auth/login/finish", body, cookie).Code != 204 ||
		request(a, "POST", "/auth/login/finish", body, cookie).Code != 401 {
		t.Fatal("proof replay")
	}
}

func TestEnrollmentRestrictionsAndFailClosed(t *testing.T) {
	for _, flags := range []byte{0x41, 0x4d} {
		a := openTest(t)
		d := newAuthenticator(t)
		options, err := a.beginEnrollment("device")
		if err != nil {
			t.Fatal(err)
		}
		response := enrollmentResponse{
			ID:       options.ID,
			Response: d.registration(t, a.enrollment[options.ID].Data.Challenge, testOrigin, flags),
		}
		if a.finishEnrollment(response) == nil || len(a.state.Devices) != 0 {
			t.Fatal("unverified or synced credential enrolled")
		}
	}
	a := openTest(t)
	d := newAuthenticator(t)
	enrollTest(t, a, d)
	cookie := loginTest(t, a, d)
	if _, err := Open(a.directory, testOrigin, testLoginPage, nil); err == nil {
		t.Fatal("second writer accepted")
	}
	if _, err := loadState(filepath.Join(a.directory, "auth.json"), "https://other.example"); err == nil {
		t.Fatal("origin switch accepted")
	}
	if err := os.Remove(filepath.Join(a.directory, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(a.directory, "auth.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if request(a, "POST", "/auth/logout", nil, cookie).Code != 503 ||
		request(a, "GET", "/", nil, cookie).Code != 401 {
		t.Fatal("failed persistence did not close authorization")
	}
	if a.SessionValid(context.Background()) {
		t.Fatal("missing socket session accepted")
	}
	for _, origin := range []string{"http://chat.example", "", "https://chat.example/"} {
		if _, err := Open(t.TempDir(), origin, testLoginPage, nil); err == nil {
			t.Fatalf("unsafe origin %q", origin)
		}
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, testOrigin, testLoginPage, nil); err == nil {
		t.Fatal("public store accepted")
	}
}

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/web/internal/bridge"
	"github.com/q15co/q15/systems/web/internal/server"
)

func proofRequest(a *Authenticator, cookie *http.Cookie, path string) *http.Request {
	r := httptest.NewRequest("GET", path, nil)
	r.AddCookie(cookie)
	signTestRequest(a, r, cookie)
	return r
}

func proofStatus(a *Authenticator, r *http.Request) int {
	w := httptest.NewRecorder()
	a.RequireScope("chat", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).
		ServeHTTP(w, r)
	return w.Code
}

func TestSessionProofFailures(t *testing.T) {
	a := openTest(t)
	d := newAuthenticator(t)
	enrollTest(t, a, d)
	cookie := loginTest(t, a, d)
	other := loginTest(t, a, d)
	second := newAuthenticator(t)
	enrollTest(t, a, second)
	otherDevice := loginTest(t, a, second)
	for _, path := range []string{"/", "/api/turns", "/ws"} {
		r := proofRequest(a, cookie, path)
		r.Header.Del(proofHeader)
		if path == "/ws" {
			r.Header.Set("Upgrade", "websocket")
		}
		if proofStatus(a, r) != 401 {
			t.Fatal("cookie alone authorized", path)
		}
	}
	cases := []struct {
		name  string
		alter func(*http.Request)
	}{
		{"malformed", func(r *http.Request) { r.Header.Set(proofHeader, "broken") }},
		{
			"duplicate",
			func(r *http.Request) { r.Header.Add(proofHeader, r.Header.Get(proofHeader)) },
		},
		{"wrong path", func(r *http.Request) { r.URL.Path = "/" }},
		{"wrong query", func(r *http.Request) { r.URL.RawQuery = "limit=1" }},
		{"wrong method", func(r *http.Request) { r.Method = "HEAD" }},
		{"other session", func(r *http.Request) { r.Header.Set("Cookie", other.String()) }},
		{"other device", func(r *http.Request) { r.Header.Set("Cookie", otherDevice.String()) }},
		{"wrong transport", func(r *http.Request) {
			r.URL.Path = "/ws"
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set(
				"Sec-WebSocket-Protocol",
				"q15-auth, "+proofProtocol+r.Header.Get(proofHeader),
			)
			r.Header.Del(proofHeader)
		}},
		{
			"stale",
			func(_ *http.Request) { a.now = func() time.Time { return time.Now().Add(proofLifetime) } },
		},
		{"future", func(_ *http.Request) {
			a.now = func() time.Time { return time.Now().Add(-proofClockSkew - time.Second) }
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			a.now = time.Now
			r := proofRequest(a, cookie, "/api/turns")
			before, err := os.ReadFile(filepath.Join(a.directory, "auth.json"))
			if err != nil {
				t.Fatal(err)
			}
			test.alter(r)
			if proofStatus(a, r) != 401 {
				t.Fatal("invalid proof authorized")
			}
			after, err := os.ReadFile(filepath.Join(a.directory, "auth.json"))
			if err != nil || string(before) != string(after) {
				t.Fatal("failed proof changed state", err)
			}
		})
	}
}

func TestSessionProofReplaySurvivesRestart(t *testing.T) {
	a := openTest(t)
	d := newAuthenticator(t)
	enrollTest(t, a, d)
	cookie := loginTest(t, a, d)
	r := proofRequest(a, cookie, "/api/turns")
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if proofStatus(a, r.Clone(context.Background())) == 204 {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("proof accepted more than once", accepted.Load())
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(a.directory, testOrigin, testLoginPage, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if proofStatus(restarted, r) != 401 {
		t.Fatal("restart reopened replay window")
	}
	if proofStatus(restarted, proofRequest(restarted, cookie, "/api/turns")) != 204 {
		t.Fatal("fresh proof denied after restart")
	}
}

func TestSessionProofReuseBoundAndExpiry(t *testing.T) {
	a := openTest(t)
	d := newAuthenticator(t)
	enrollTest(t, a, d)
	cookie := loginTest(t, a, d)
	for range maxProofs {
		if proofStatus(a, proofRequest(a, cookie, "/")) != 204 {
			t.Fatal("fresh proof refused")
		}
	}
	if proofStatus(a, proofRequest(a, cookie, "/")) != 401 {
		t.Fatal("reuse table grew past bound")
	}
	if len(a.state.Sessions[digest(cookie.Value)].Used) != maxProofs {
		t.Fatal("incorrect reuse bound")
	}
	a.now = func() time.Time { return time.Now().Add(proofLifetime + time.Second) }
	if proofStatus(a, proofRequest(a, cookie, "/")) != 204 {
		t.Fatal("expired reuse records blocked fresh proof")
	}
	if len(a.state.Sessions[digest(cookie.Value)].Used) != 1 {
		t.Fatal("expired reuse records retained")
	}
}

func TestBearerSessionMigration(t *testing.T) {
	a := openTest(t)
	d := newAuthenticator(t)
	enrollTest(t, a, d)
	cookie := loginTest(t, a, d)
	old := a.state
	old.Version = 1
	if err := a.commit(old); err != nil {
		t.Fatal(err)
	}
	migrated, err := loadState(filepath.Join(a.directory, "auth.json"), testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Version != 2 || len(migrated.Sessions) != 0 || len(migrated.Devices) != 1 {
		t.Fatal("bearer sessions survived migration")
	}
	if cookie.Value == "" {
		t.Fatal("missing migration fixture")
	}
}

type proofBridge struct{ bridge.Service }

func (proofBridge) ListTurns(
	context.Context,
	*chatpb.ListTurnsRequest,
) (*chatpb.ListTurnsResponse, error) {
	return &chatpb.ListTurnsResponse{}, nil
}
func (proofBridge) Deliver(ctx context.Context) (bridge.DeliverStream, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSocketProofAndDeviceRevocation(t *testing.T) {
	a := openTest(t)
	d := newAuthenticator(t)
	id := enrollTest(t, a, d)
	cookie := loginTest(t, a, d)
	s, err := server.New(
		context.Background(),
		proofBridge{},
		server.Config{Origin: testOrigin, Authorizer: a, Assets: http.NotFoundHandler()},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := proofRequest(a, cookie, "/ws")
	r.Header.Set("Upgrade", "websocket")
	signTestRequest(a, r, cookie)
	proof := r.Header.Get(proofHeader)
	headers := http.Header{"Origin": {testOrigin}, "Cookie": {cookie.String()}}
	address := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	_, response, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPHeader: headers})
	if err == nil || response == nil || response.StatusCode != 401 {
		t.Fatal("socket accepted cookie alone")
	}
	options := &websocket.DialOptions{
		HTTPHeader:   headers,
		Subprotocols: []string{"q15-auth", proofProtocol + proof},
	}
	conn, _, err := websocket.Dial(ctx, address, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if conn.Subprotocol() != "q15-auth" {
		t.Fatal("proof echoed as a negotiated protocol")
	}
	_, response, err = websocket.Dial(ctx, address, options)
	if err == nil || response == nil || response.StatusCode != 401 {
		t.Fatal("socket accepted replayed proof")
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"v":1,"id":"hello","type":"hello","ts":"2026-10-03T00:00:00Z","seq":"0","payload":{"cursor":"0"}}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &frame) != nil || frame.Type != "ready" {
		t.Fatal("socket not ready", string(data))
	}
	if err := a.revoke(id); err != nil {
		t.Fatal(err)
	}
	if proofStatus(a, proofRequest(a, cookie, "/")) != 401 {
		t.Fatal("revoked request accepted")
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusCode(4401) {
		t.Fatal("revocation did not close idle socket", err)
	}
}

func TestWorkerBootstrapProofIsSingleUseAndConfined(t *testing.T) {
	a := openTest(t)
	d := newAuthenticator(t)
	enrollTest(t, a, d)
	cookie := loginTest(t, a, d)
	proof := proofRequest(a, cookie, "/sw.js").Header.Get(proofHeader)
	w := request(a, "POST", "/auth/worker", marshal(t, map[string]string{"proof": proof}), cookie)
	if w.Code != 204 {
		t.Fatal("worker bootstrap failed", w.Code)
	}
	bootstrap := findCookie(t, w, workerCookie)
	if !bootstrap.HttpOnly || !bootstrap.Secure || bootstrap.MaxAge != 60 {
		t.Fatal("unsafe worker proof cookie")
	}
	for _, path := range []string{"/", "/api/turns", "/sw.js?other=1"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		r.AddCookie(bootstrap)
		if proofStatus(a, r) != 401 {
			t.Fatal("worker proof escaped its target")
		}
	}
	for _, status := range []int{204, 401} {
		r := httptest.NewRequest("GET", "/sw.js", nil)
		r.AddCookie(cookie)
		r.AddCookie(bootstrap)
		if proofStatus(a, r) != status {
			t.Fatal("incorrect bootstrap consumption")
		}
	}
	for _, body := range [][]byte{[]byte(`not json`), []byte(`{"proof":"p","other":true}`), []byte(`{"proof":"p"} {}`)} {
		if request(a, "POST", "/auth/worker", body, cookie).Code != 401 {
			t.Fatal("invalid bootstrap accepted")
		}
	}
}

func TestLoginRefusesMalformedSessionKey(t *testing.T) {
	a := openTest(t)
	for _, body := range [][]byte{[]byte(`null`), []byte(`{"public_key":"invalid"}`), []byte(`{"public_key":"invalid","extra":true}`), []byte(`{"public_key":"invalid"} {}`)} {
		if request(a, "POST", "/auth/login", body).Code != 401 || len(a.login) != 0 ||
			len(a.state.Sessions) != 0 {
			t.Fatal("invalid key changed login state")
		}
	}
}

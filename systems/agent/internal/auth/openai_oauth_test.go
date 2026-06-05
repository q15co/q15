package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func withOpenAIHTTPClient(t *testing.T, client *http.Client) {
	t.Helper()

	prev := openAIHTTPClient
	openAIHTTPClient = client
	t.Cleanup(func() { openAIHTTPClient = prev })
}

func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()

	header := `{"alg":"none","typ":"JWT"}`
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}

	headerPart := base64.RawURLEncoding.EncodeToString([]byte(header))
	payloadPart := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return headerPart + "." + payloadPart + "."
}

func TestParseDeviceCodeResponseSupportsIntInterval(t *testing.T) {
	resp, err := parseDeviceCodeResponse([]byte(`{
		"device_auth_id": "dev-1",
		"user_code": "ABCD",
		"interval": 7
	}`))
	if err != nil {
		t.Fatalf("parseDeviceCodeResponse error = %v", err)
	}
	if resp.Interval != 7 {
		t.Fatalf("interval = %d, want 7", resp.Interval)
	}
	if resp.DeviceAuthID != "dev-1" {
		t.Fatalf("device_auth_id = %q, want %q", resp.DeviceAuthID, "dev-1")
	}
}

func TestParseDeviceCodeResponseSupportsStringInterval(t *testing.T) {
	resp, err := parseDeviceCodeResponse([]byte(`{
		"device_auth_id": "dev-1",
		"user_code": "ABCD",
		"interval": "5"
	}`))
	if err != nil {
		t.Fatalf("parseDeviceCodeResponse error = %v", err)
	}
	if resp.Interval != 5 {
		t.Fatalf("interval = %d, want 5", resp.Interval)
	}
}

func TestExtractAccountIDVariants(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   string
	}{
		{
			name: "direct chatgpt_account_id",
			claims: map[string]any{
				"chatgpt_account_id": "acc-1",
			},
			want: "acc-1",
		},
		{
			name: "namespaced account id",
			claims: map[string]any{
				"https://api.openai.com/auth.chatgpt_account_id": "acc-2",
			},
			want: "acc-2",
		},
		{
			name: "nested auth account id",
			claims: map[string]any{
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id": "acc-3",
				},
			},
			want: "acc-3",
		},
		{
			name: "organizations fallback",
			claims: map[string]any{
				"organizations": []any{
					map[string]any{"id": "org-123"},
				},
			},
			want: "org-123",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			token := makeJWT(t, tc.claims)
			if got := extractAccountID(token); got != tc.want {
				t.Fatalf("extractAccountID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRefreshOpenAITokenDoesNotReuseMissingRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("unexpected path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"access_token": "new-access-token",
			"expires_in": 3600
		}`))
	}))
	defer srv.Close()

	cred, err := refreshOpenAIToken(context.Background(), openAIOAuthConfig{
		Issuer:     srv.URL,
		ClientID:   "client-id",
		Originator: "codex_cli_rs",
	}, &Credential{
		AccessToken:  "old-access-token",
		RefreshToken: "old-refresh-token",
		AccountID:    "acc-123",
		Provider:     "openai",
		AuthMethod:   "oauth",
	})
	if err != nil {
		t.Fatalf("refreshOpenAIToken error = %v", err)
	}
	if cred.AccessToken != "new-access-token" {
		t.Fatalf("access token = %q, want %q", cred.AccessToken, "new-access-token")
	}
	if cred.RefreshToken != "" {
		t.Fatalf("refresh token = %q, want empty", cred.RefreshToken)
	}
	if cred.AccountID != "acc-123" {
		t.Fatalf("account id = %q, want %q", cred.AccountID, "acc-123")
	}
}

func TestLoadOpenAITokenSerializesConcurrentRefresh(t *testing.T) {
	_ = withTestStorePath(t)

	if err := SaveStore(&Store{
		Credentials: map[string]*Credential{
			"openai": {
				AccessToken:  "old-access-token",
				RefreshToken: "old-refresh-token",
				AccountID:    "acc-123",
				ExpiresAt:    time.Now().Add(-1 * time.Minute),
				Provider:     "openai",
				AuthMethod:   "oauth",
			},
		},
	}); err != nil {
		t.Fatalf("SaveStore error = %v", err)
	}

	var refreshCalls atomic.Int32
	withOpenAIHTTPClient(
		t,
		&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/oauth/token" {
				return nil, fmt.Errorf("unexpected path = %q", req.URL.Path)
			}
			reqBody, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			if !strings.Contains(string(reqBody), "refresh_token=old-refresh-token") {
				return nil, fmt.Errorf("refresh request body = %q", string(reqBody))
			}
			if refreshCalls.Add(1) > 1 {
				return responseWithBody(
					http.StatusBadRequest,
					`{"error":"refresh token reused"}`,
				), nil
			}

			return responseWithBody(http.StatusOK, `{
			"access_token": "new-access-token",
			"refresh_token": "new-refresh-token",
			"expires_in": 3600
		}`), nil
		})},
	)

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			accessToken, accountID, err := LoadOpenAIToken(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if accessToken != "new-access-token" {
				errs <- fmt.Errorf("access token = %q, want new-access-token", accessToken)
				return
			}
			if accountID != "acc-123" {
				errs <- fmt.Errorf("account id = %q, want acc-123", accountID)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("LoadOpenAIToken error = %v", err)
		}
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}

	store, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore error = %v", err)
	}
	if got := store.Credentials["openai"].RefreshToken; got != "new-refresh-token" {
		t.Fatalf("stored refresh token = %q, want new-refresh-token", got)
	}
}

func TestLoadOpenAITokenClearsRejectedRefreshToken(t *testing.T) {
	_ = withTestStorePath(t)

	if err := SaveStore(&Store{
		Credentials: map[string]*Credential{
			"openai": {
				AccessToken:  "old-access-token",
				RefreshToken: "dead-refresh-token",
				AccountID:    "acc-123",
				ExpiresAt:    time.Now().Add(-1 * time.Minute),
				Provider:     "openai",
				AuthMethod:   "oauth",
			},
		},
	}); err != nil {
		t.Fatalf("SaveStore error = %v", err)
	}

	withOpenAIHTTPClient(
		t,
		&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/oauth/token" {
				return nil, fmt.Errorf("unexpected path = %q", req.URL.Path)
			}
			return responseWithBody(http.StatusBadRequest, `{
			"error": {
				"message": "Your refresh token has already been used to generate a new access token.",
				"type": "invalidrequesterror",
				"code": "refreshtokenreused"
			}
		}`), nil
		})},
	)

	_, _, err := LoadOpenAIToken(context.Background())
	if err == nil {
		t.Fatalf("expected refresh error")
	}
	if !strings.Contains(err.Error(), "refreshtokenreused") {
		t.Fatalf("error = %v, want refreshtokenreused", err)
	}

	store, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore error = %v", err)
	}
	cred := store.Credentials["openai"]
	if cred == nil {
		t.Fatalf("stored openai credential missing")
	}
	if cred.RefreshToken != "" {
		t.Fatalf("stored refresh token = %q, want empty", cred.RefreshToken)
	}
	if cred.AccessToken != "old-access-token" {
		t.Fatalf("stored access token = %q, want old-access-token", cred.AccessToken)
	}

	_, _, err = LoadOpenAIToken(context.Background())
	if err == nil {
		t.Fatalf("expected missing refresh token error")
	}
	if !strings.Contains(err.Error(), "no refresh token is available") {
		t.Fatalf("error = %v, want no refresh token", err)
	}
}

func responseWithBody(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestPollOpenAIDeviceCodeOnceTreats403AsPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/token" {
			t.Fatalf("unexpected path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{
			"error": {
				"message": "Device authorization is unknown. Please try again.",
				"code": "deviceauth_authorization_unknown"
			}
		}`))
	}))
	defer srv.Close()

	_, err := pollOpenAIDeviceCodeOnce(context.Background(), openAIOAuthConfig{
		Issuer:     srv.URL,
		ClientID:   "client-id",
		Originator: "codex_cli_rs",
	}, "device-auth-123", "CODE-123")
	if err == nil {
		t.Fatalf("expected pending error")
	}
	if err != ErrAuthorizationPending {
		t.Fatalf("error = %v, want %v", err, ErrAuthorizationPending)
	}
}

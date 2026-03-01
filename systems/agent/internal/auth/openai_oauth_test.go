package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestRefreshOpenAITokenPreservesExistingRefreshAndAccountID(t *testing.T) {
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
	if cred.RefreshToken != "old-refresh-token" {
		t.Fatalf("refresh token = %q, want %q", cred.RefreshToken, "old-refresh-token")
	}
	if cred.AccountID != "acc-123" {
		t.Fatalf("account id = %q, want %q", cred.AccountID, "acc-123")
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

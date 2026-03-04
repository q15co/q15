// Package auth handles persistent provider credentials and OpenAI device-flow
// authentication.
package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	openAIOAuthIssuer       = "https://auth.openai.com"
	openAIOAuthClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAIOAuthOriginator   = "codex_cli_rs"
	openAIDeviceRedirectURI = "https://auth.openai.com/deviceauth/callback"
	defaultDeviceAuthWindow = 15 * time.Minute
)

var (
	// ErrAuthorizationPending indicates device login is still awaiting user approval.
	ErrAuthorizationPending = errors.New("authorization pending")
	openAIHTTPClient        = &http.Client{Timeout: 15 * time.Second}
)

type openAIOAuthConfig struct {
	Issuer     string
	ClientID   string
	Originator string
}

// DeviceCodeInfo contains the user-facing data needed to complete device auth.
type DeviceCodeInfo struct {
	// DeviceAuthID identifies the in-progress device authorization session.
	DeviceAuthID string `json:"device_auth_id"`
	// UserCode is the short code the user enters in the browser.
	UserCode string `json:"user_code"`
	// VerifyURL is the URL where the user completes authorization.
	VerifyURL string `json:"verify_url"`
	// Interval is the recommended polling interval in seconds.
	Interval int `json:"interval"`
}

type deviceCodeResponse struct {
	DeviceAuthID string
	UserCode     string
	Interval     int
}

func defaultOpenAIOAuthConfig() openAIOAuthConfig {
	return openAIOAuthConfig{
		Issuer:     openAIOAuthIssuer,
		ClientID:   openAIOAuthClientID,
		Originator: openAIOAuthOriginator,
	}
}

// LoginOpenAIDeviceCode performs OpenAI OAuth device login and returns the
// resulting credential on success.
func LoginOpenAIDeviceCode(ctx context.Context, out io.Writer) (*Credential, error) {
	return loginOpenAIDeviceCode(ctx, out, defaultOpenAIOAuthConfig())
}

func loginOpenAIDeviceCode(
	ctx context.Context,
	out io.Writer,
	cfg openAIOAuthConfig,
) (*Credential, error) {
	if out == nil {
		out = io.Discard
	}

	info, err := requestOpenAIDeviceCode(ctx, cfg)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(
		out,
		"Open this URL in your browser:\n\n  %s\n\nThen enter this code: %s\n\nWaiting for authentication...\n",
		info.VerifyURL,
		info.UserCode,
	)

	timeout := time.NewTimer(defaultDeviceAuthWindow)
	defer timeout.Stop()
	ticker := time.NewTicker(time.Duration(info.Interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, fmt.Errorf(
				"device authentication timed out after %s",
				defaultDeviceAuthWindow,
			)
		case <-ticker.C:
			cred, err := pollOpenAIDeviceCodeOnce(ctx, cfg, info.DeviceAuthID, info.UserCode)
			if err == nil {
				return cred, nil
			}
			if errors.Is(err, ErrAuthorizationPending) {
				continue
			}
			return nil, err
		}
	}
}

// RequestOpenAIDeviceCode requests a new OpenAI device code challenge.
func RequestOpenAIDeviceCode(ctx context.Context) (*DeviceCodeInfo, error) {
	return requestOpenAIDeviceCode(ctx, defaultOpenAIOAuthConfig())
}

func requestOpenAIDeviceCode(ctx context.Context, cfg openAIOAuthConfig) (*DeviceCodeInfo, error) {
	reqBody, _ := json.Marshal(map[string]string{
		"client_id": cfg.ClientID,
	})
	body, statusCode, err := doOpenAIRequest(
		ctx,
		http.MethodPost,
		cfg.Issuer+"/api/accounts/deviceauth/usercode",
		"application/json",
		bytes.NewReader(reqBody),
		cfg.Originator,
	)
	if err != nil {
		return nil, fmt.Errorf("requesting openai device code: %w", err)
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"openai device code request failed: %s",
			strings.TrimSpace(string(body)),
		)
	}

	parsed, err := parseDeviceCodeResponse(body)
	if err != nil {
		return nil, fmt.Errorf("parse openai device code response: %w", err)
	}
	if parsed.Interval < 1 {
		parsed.Interval = 5
	}

	return &DeviceCodeInfo{
		DeviceAuthID: parsed.DeviceAuthID,
		UserCode:     parsed.UserCode,
		VerifyURL:    cfg.Issuer + "/codex/device",
		Interval:     parsed.Interval,
	}, nil
}

// RefreshOpenAIToken uses a refresh token to obtain a new OpenAI access token.
func RefreshOpenAIToken(ctx context.Context, cred *Credential) (*Credential, error) {
	return refreshOpenAIToken(ctx, defaultOpenAIOAuthConfig(), cred)
}

func refreshOpenAIToken(
	ctx context.Context,
	cfg openAIOAuthConfig,
	cred *Credential,
) (*Credential, error) {
	if cred == nil {
		return nil, fmt.Errorf("credential is required")
	}
	if strings.TrimSpace(cred.RefreshToken) == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	data := url.Values{
		"client_id":     {cfg.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {cred.RefreshToken},
		"scope":         {"openid profile email offline_access"},
	}
	body, statusCode, err := doOpenAIRequest(
		ctx,
		http.MethodPost,
		cfg.Issuer+"/oauth/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(data.Encode()),
		cfg.Originator,
	)
	if err != nil {
		return nil, fmt.Errorf("refresh openai token: %w", err)
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("openai token refresh failed: %s", strings.TrimSpace(string(body)))
	}

	refreshed, err := parseTokenResponse(body, "openai")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(refreshed.RefreshToken) == "" {
		refreshed.RefreshToken = cred.RefreshToken
	}
	if strings.TrimSpace(refreshed.AccountID) == "" {
		refreshed.AccountID = cred.AccountID
	}
	return refreshed, nil
}

// LoadOpenAIToken loads and refreshes persisted OpenAI credentials as needed,
// returning access token and account ID.
func LoadOpenAIToken(ctx context.Context) (string, string, error) {
	cred, err := resolveOpenAICredential(ctx)
	if err != nil {
		return "", "", err
	}
	return cred.AccessToken, cred.AccountID, nil
}

func resolveOpenAICredential(ctx context.Context) (*Credential, error) {
	cred, err := GetCredential("openai")
	if err != nil {
		return nil, fmt.Errorf("load q15 auth credential: %w", err)
	}
	if cred == nil {
		return nil, fmt.Errorf("no credentials for openai. Run: q15 auth login --provider openai")
	}
	if strings.TrimSpace(cred.AccessToken) == "" {
		return nil, fmt.Errorf("openai credential has empty access token")
	}
	if !cred.NeedsRefresh() || strings.TrimSpace(cred.RefreshToken) == "" {
		return cred, nil
	}

	refreshed, err := RefreshOpenAIToken(ctx, cred)
	if err != nil {
		return nil, err
	}
	if err := SetCredential("openai", refreshed); err != nil {
		return nil, fmt.Errorf("save refreshed openai credential: %w", err)
	}
	return refreshed, nil
}

func pollOpenAIDeviceCodeOnce(
	ctx context.Context,
	cfg openAIOAuthConfig,
	deviceAuthID string,
	userCode string,
) (*Credential, error) {
	reqBody, _ := json.Marshal(map[string]string{
		"device_auth_id": deviceAuthID,
		"user_code":      userCode,
	})
	body, statusCode, err := doOpenAIRequest(
		ctx,
		http.MethodPost,
		cfg.Issuer+"/api/accounts/deviceauth/token",
		"application/json",
		bytes.NewReader(reqBody),
		cfg.Originator,
	)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		if statusCode == http.StatusForbidden || statusCode == http.StatusNotFound {
			return nil, ErrAuthorizationPending
		}
		lower := strings.ToLower(string(body))
		if strings.Contains(lower, "pending") || strings.Contains(lower, "slow_down") {
			return nil, ErrAuthorizationPending
		}
		return nil, fmt.Errorf(
			"openai device token poll failed: %s",
			strings.TrimSpace(string(body)),
		)
	}

	var tokenResp struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse openai device token response: %w", err)
	}
	if strings.TrimSpace(tokenResp.AuthorizationCode) == "" {
		return nil, fmt.Errorf("openai device token response missing authorization_code")
	}
	if strings.TrimSpace(tokenResp.CodeVerifier) == "" {
		return nil, fmt.Errorf("openai device token response missing code_verifier")
	}

	return exchangeCodeForTokens(
		ctx,
		cfg,
		tokenResp.AuthorizationCode,
		tokenResp.CodeVerifier,
		openAIDeviceRedirectURI,
	)
}

func exchangeCodeForTokens(
	ctx context.Context,
	cfg openAIOAuthConfig,
	code string,
	codeVerifier string,
	redirectURI string,
) (*Credential, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {cfg.ClientID},
		"code_verifier": {codeVerifier},
	}
	body, statusCode, err := doOpenAIRequest(
		ctx,
		http.MethodPost,
		cfg.Issuer+"/oauth/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(data.Encode()),
		cfg.Originator,
	)
	if err != nil {
		return nil, fmt.Errorf("exchange openai auth code: %w", err)
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("openai token exchange failed: %s", strings.TrimSpace(string(body)))
	}
	return parseTokenResponse(body, "openai")
}

func doOpenAIRequest(
	ctx context.Context,
	method string,
	endpoint string,
	contentType string,
	body io.Reader,
	originator string,
) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if strings.TrimSpace(originator) != "" {
		req.Header.Set("Originator", originator)
	}

	resp, err := openAIHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return respBody, resp.StatusCode, nil
}

func parseDeviceCodeResponse(body []byte) (deviceCodeResponse, error) {
	var raw struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return deviceCodeResponse{}, err
	}
	interval, err := parseFlexibleInt(raw.Interval)
	if err != nil {
		return deviceCodeResponse{}, err
	}
	return deviceCodeResponse{
		DeviceAuthID: raw.DeviceAuthID,
		UserCode:     raw.UserCode,
		Interval:     interval,
	}, nil
}

func parseFlexibleInt(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}

	var intValue int
	if err := json.Unmarshal(raw, &intValue); err == nil {
		return intValue, nil
	}

	var stringValue string
	if err := json.Unmarshal(raw, &stringValue); err == nil {
		stringValue = strings.TrimSpace(stringValue)
		if stringValue == "" {
			return 0, nil
		}
		return strconv.Atoi(stringValue)
	}

	return 0, fmt.Errorf("invalid integer value: %s", string(raw))
}

func parseTokenResponse(body []byte, provider string) (*Credential, error) {
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}

	var expiresAt time.Time
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}

	cred := &Credential{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    expiresAt,
		Provider:     provider,
		AuthMethod:   "oauth",
	}

	if accountID := extractAccountID(tokenResp.IDToken); accountID != "" {
		cred.AccountID = accountID
	} else if accountID := extractAccountID(tokenResp.AccessToken); accountID != "" {
		cred.AccountID = accountID
	}
	return cred, nil
}

func extractAccountID(token string) string {
	claims, err := parseJWTClaims(token)
	if err != nil {
		return ""
	}

	if accountID, ok := claims["chatgpt_account_id"].(string); ok && accountID != "" {
		return accountID
	}
	if accountID, ok := claims["https://api.openai.com/auth.chatgpt_account_id"].(string); ok &&
		accountID != "" {
		return accountID
	}
	if authClaim, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if accountID, ok := authClaim["chatgpt_account_id"].(string); ok && accountID != "" {
			return accountID
		}
	}
	if orgs, ok := claims["organizations"].([]any); ok {
		for _, org := range orgs {
			orgMap, ok := org.(map[string]any)
			if !ok {
				continue
			}
			if accountID, ok := orgMap["id"].(string); ok && accountID != "" {
				return accountID
			}
		}
	}

	return ""
}

func parseJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("token is not a jwt")
	}

	payload := parts[1]
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(payload)
		if err != nil {
			return nil, err
		}
	}

	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

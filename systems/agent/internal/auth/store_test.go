package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withTestStorePath(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "auth.json")
	prev := authStorePath
	authStorePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { authStorePath = prev })
	return path
}

func TestLoadStoreMissingFileReturnsEmptyStore(t *testing.T) {
	_ = withTestStorePath(t)

	store, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore error = %v", err)
	}
	if store == nil {
		t.Fatalf("LoadStore returned nil store")
	}
	if len(store.Credentials) != 0 {
		t.Fatalf("credentials len = %d, want 0", len(store.Credentials))
	}
}

func TestSaveStoreRoundTripAndFileMode(t *testing.T) {
	path := withTestStorePath(t)

	in := &Store{
		Credentials: map[string]*Credential{
			"openai": {
				AccessToken:  "access-token",
				RefreshToken: "refresh-token",
				AccountID:    "acc-123",
				ExpiresAt:    time.Now().Add(30 * time.Minute).Round(time.Second),
				Provider:     "openai",
				AuthMethod:   "oauth",
			},
		},
	}
	if err := SaveStore(in); err != nil {
		t.Fatalf("SaveStore error = %v", err)
	}

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat auth store error = %v", err)
	}
	if got := stat.Mode().Perm(); got != 0o600 {
		t.Fatalf("auth store mode = %o, want 600", got)
	}

	out, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore error = %v", err)
	}
	cred := out.Credentials["openai"]
	if cred == nil {
		t.Fatalf("openai credential missing after roundtrip")
	}
	if cred.AccessToken != "access-token" {
		t.Fatalf("access token = %q, want %q", cred.AccessToken, "access-token")
	}
	if cred.RefreshToken != "refresh-token" {
		t.Fatalf("refresh token = %q, want %q", cred.RefreshToken, "refresh-token")
	}
	if cred.AccountID != "acc-123" {
		t.Fatalf("account id = %q, want %q", cred.AccountID, "acc-123")
	}
}

func TestGetSetDeleteCredential(t *testing.T) {
	_ = withTestStorePath(t)

	cred := &Credential{
		AccessToken: "token-1",
		AuthMethod:  "oauth",
	}
	if err := SetCredential("openai", cred); err != nil {
		t.Fatalf("SetCredential error = %v", err)
	}

	got, err := GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential error = %v", err)
	}
	if got == nil {
		t.Fatalf("GetCredential returned nil credential")
	}
	if got.Provider != "openai" {
		t.Fatalf("provider = %q, want %q", got.Provider, "openai")
	}
	if got.AccessToken != "token-1" {
		t.Fatalf("access token = %q, want %q", got.AccessToken, "token-1")
	}

	if err := DeleteCredential("openai"); err != nil {
		t.Fatalf("DeleteCredential error = %v", err)
	}

	got, err = GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential after delete error = %v", err)
	}
	if got != nil {
		t.Fatalf("credential still exists after delete")
	}
}

func TestDeleteAllCredentials(t *testing.T) {
	path := withTestStorePath(t)

	if err := SaveStore(&Store{
		Credentials: map[string]*Credential{
			"openai": {AccessToken: "token", Provider: "openai", AuthMethod: "oauth"},
		},
	}); err != nil {
		t.Fatalf("SaveStore error = %v", err)
	}

	if err := DeleteAllCredentials(); err != nil {
		t.Fatalf("DeleteAllCredentials error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("auth store still exists, stat err = %v", err)
	}
}

func TestCredentialExpiryHelpers(t *testing.T) {
	if (&Credential{}).IsExpired() {
		t.Fatalf("zero expiry should not be expired")
	}
	if (&Credential{}).NeedsRefresh() {
		t.Fatalf("zero expiry should not need refresh")
	}

	expired := &Credential{ExpiresAt: time.Now().Add(-1 * time.Minute)}
	if !expired.IsExpired() {
		t.Fatalf("expired credential should be expired")
	}
	if !expired.NeedsRefresh() {
		t.Fatalf("expired credential should need refresh")
	}

	soon := &Credential{ExpiresAt: time.Now().Add(4 * time.Minute)}
	if !soon.NeedsRefresh() {
		t.Fatalf("credential expiring in <5m should need refresh")
	}
}

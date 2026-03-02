package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	q15paths "github.com/q15co/q15/systems/agent/internal/paths"
)

type Credential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Provider     string    `json:"provider"`
	AuthMethod   string    `json:"auth_method"`
}

type Store struct {
	Credentials map[string]*Credential `json:"credentials"`
}

var authStorePath = defaultAuthStorePath

func (c *Credential) IsExpired() bool {
	if c == nil || c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}

func (c *Credential) NeedsRefresh() bool {
	if c == nil || c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(5 * time.Minute).After(c.ExpiresAt)
}

func SetStorePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("auth store path is required")
	}
	authStorePath = func() (string, error) { return path, nil }
	return nil
}

func defaultAuthStorePath() (string, error) {
	return q15paths.DefaultAuthPath()
}

func LoadStore() (*Store, error) {
	path, err := authStorePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Store{Credentials: map[string]*Credential{}}, nil
		}
		return nil, fmt.Errorf("read auth store %q: %w", path, err)
	}

	var store Store
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("decode auth store %q: %w", path, err)
	}
	if store.Credentials == nil {
		store.Credentials = map[string]*Credential{}
	}
	return &store, nil
}

func SaveStore(store *Store) error {
	if store == nil {
		store = &Store{}
	}
	if store.Credentials == nil {
		store.Credentials = map[string]*Credential{}
	}

	path, err := authStorePath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("encode auth store: %w", err)
	}
	data = append(data, '\n')

	return writeFileAtomic(path, data, 0o600)
}

func GetCredential(provider string) (*Credential, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, fmt.Errorf("provider is required")
	}

	store, err := LoadStore()
	if err != nil {
		return nil, err
	}

	cred, ok := store.Credentials[provider]
	if !ok {
		return nil, nil
	}
	return cred, nil
}

func SetCredential(provider string, cred *Credential) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if cred == nil {
		return fmt.Errorf("credential is required")
	}

	store, err := LoadStore()
	if err != nil {
		return err
	}

	cred.Provider = provider
	if strings.TrimSpace(cred.AuthMethod) == "" {
		cred.AuthMethod = "oauth"
	}
	store.Credentials[provider] = cred
	return SaveStore(store)
}

func DeleteCredential(provider string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("provider is required")
	}

	store, err := LoadStore()
	if err != nil {
		return err
	}

	delete(store.Credentials, provider)
	return SaveStore(store)
}

func DeleteAllCredentials() error {
	path, err := authStorePath()
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove auth store %q: %w", path, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create auth store dir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".auth-*.tmp")
	if err != nil {
		return fmt.Errorf("create auth store temp file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write auth store temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync auth store temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close auth store temp file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return fmt.Errorf("chmod auth store temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace auth store file: %w", err)
	}
	return nil
}

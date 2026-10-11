package media

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SweepWeb releases old upload scopes absent from the durable transcript.
// Other channels' scopes and recent interrupted sends keep their own lifetime.
func (s *FileStore) SweepWeb(keep map[string]struct{}, before time.Time) error {
	s.mu.Lock()
	scopes, err := s.expiredWebScopes(keep, before)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if err := s.ReleaseAll(scope); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileStore) expiredWebScopes(keep map[string]struct{}, before time.Time) ([]string, error) {
	entries, err := os.ReadDir(s.scopesDir())
	if err != nil {
		return nil, err
	}
	var scopes []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.ModTime().Before(before) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.scopesDir(), entry.Name()))
		if err != nil {
			return nil, err
		}
		var record scopeRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(record.Scope, "web-conversation:") {
			continue
		}
		used := false
		for _, ref := range record.Refs {
			if _, ok := keep[ref]; ok {
				used = true
				break
			}
		}
		if !used {
			scopes = append(scopes, record.Scope)
		}
	}
	return scopes, nil
}

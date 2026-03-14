package proxy

import (
	"fmt"
	"regexp"
	"strings"
)

var secretTemplateRE = regexp.MustCompile(`\{\{\s*secret\.([a-z0-9_-]+)\s*\}\}`)

func renderSecretTemplate(raw string, secretValues map[string]string) (string, error) {
	if !strings.Contains(raw, "{{") {
		return raw, nil
	}

	var firstErr error
	out := secretTemplateRE.ReplaceAllStringFunc(raw, func(match string) string {
		if firstErr != nil {
			return match
		}
		sub := secretTemplateRE.FindStringSubmatch(match)
		if len(sub) != 2 {
			firstErr = fmt.Errorf("invalid secret template %q", match)
			return match
		}
		alias := strings.ToLower(strings.TrimSpace(sub[1]))
		value := strings.TrimSpace(secretValues[alias])
		if value == "" {
			firstErr = fmt.Errorf("missing secret value for alias %q", alias)
			return match
		}
		return value
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

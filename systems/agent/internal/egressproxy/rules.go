package egressproxy

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"strings"
)

// Rule describes one host/path-matched request mutation applied by the proxy.
type Rule struct {
	Name               string
	MatchHosts         []string
	MatchPathPrefixes  []string
	SetHeader          map[string]string
	SetBasicAuth       *BasicAuth
	ReplacePlaceholder []PlaceholderReplacement
}

// BasicAuth injects an Authorization Basic header from a named secret.
type BasicAuth struct {
	Username string
	Secret   string
}

// PlaceholderReplacement swaps an opaque placeholder for a named secret value.
type PlaceholderReplacement struct {
	Placeholder string
	Secret      string
	In          []string
}

type compiledRule struct {
	name               string
	matchHosts         map[string]struct{}
	matchPathPrefixes  []string
	setHeader          map[string]string
	setBasicAuthHeader string
	replacePlaceholder []compiledPlaceholderReplacement
}

type compiledPlaceholderReplacement struct {
	placeholder string
	secretValue string
	inHeader    bool
	inQuery     bool
	inPath      bool
}

func compileRules(rules []Rule, secretValues map[string]string) ([]compiledRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}

	out := make([]compiledRule, 0, len(rules))
	for i, rule := range rules {
		compiled := compiledRule{
			name:              strings.TrimSpace(rule.Name),
			matchHosts:        make(map[string]struct{}, len(rule.MatchHosts)),
			matchPathPrefixes: normalizePathPrefixes(rule.MatchPathPrefixes),
		}
		for _, rawHost := range rule.MatchHosts {
			host := normalizeHostForMatch(rawHost)
			if host == "" {
				continue
			}
			compiled.matchHosts[host] = struct{}{}
		}
		if len(compiled.matchHosts) == 0 {
			return nil, fmt.Errorf("rule[%d] has no usable match hosts", i)
		}

		if len(rule.SetHeader) > 0 {
			compiled.setHeader = make(map[string]string, len(rule.SetHeader))
			hasAuthorizationHeader := false
			for k, v := range rule.SetHeader {
				headerName := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(k))
				if headerName == "" {
					return nil, fmt.Errorf("rule[%d].set_header contains an empty header name", i)
				}
				if _, err := renderSecretTemplate(v, secretValues); err != nil {
					return nil, fmt.Errorf("rule[%d].set_header[%q]: %w", i, k, err)
				}
				if headerName == "Authorization" {
					hasAuthorizationHeader = true
				}
				compiled.setHeader[headerName] = v
			}
			if hasAuthorizationHeader && rule.SetBasicAuth != nil {
				return nil, fmt.Errorf(
					"rule[%d] cannot set both set_basic_auth and set_header.Authorization",
					i,
				)
			}
		}
		if rule.SetBasicAuth != nil {
			username := strings.TrimSpace(rule.SetBasicAuth.Username)
			if username == "" {
				return nil, fmt.Errorf("rule[%d].set_basic_auth.username is required", i)
			}
			secretAlias := strings.ToLower(strings.TrimSpace(rule.SetBasicAuth.Secret))
			secretValue := strings.TrimSpace(secretValues[secretAlias])
			if secretValue == "" {
				return nil, fmt.Errorf(
					"rule[%d].set_basic_auth.secret missing value for alias %q",
					i,
					secretAlias,
				)
			}
			compiled.setBasicAuthHeader = "Basic " + base64.StdEncoding.EncodeToString(
				[]byte(username+":"+secretValue),
			)
		}
		if len(rule.ReplacePlaceholder) > 0 {
			compiled.replacePlaceholder = make(
				[]compiledPlaceholderReplacement,
				0,
				len(rule.ReplacePlaceholder),
			)
			for j, repl := range rule.ReplacePlaceholder {
				secretAlias := strings.ToLower(strings.TrimSpace(repl.Secret))
				secretValue := strings.TrimSpace(secretValues[secretAlias])
				if secretValue == "" {
					return nil, fmt.Errorf(
						"rule[%d].replace_placeholder[%d].secret missing value for alias %q",
						i,
						j,
						secretAlias,
					)
				}
				compiledRepl := compiledPlaceholderReplacement{
					placeholder: strings.TrimSpace(repl.Placeholder),
					secretValue: secretValue,
				}
				for _, where := range repl.In {
					switch strings.ToLower(strings.TrimSpace(where)) {
					case "header":
						compiledRepl.inHeader = true
					case "query":
						compiledRepl.inQuery = true
					case "path":
						compiledRepl.inPath = true
					}
				}
				compiled.replacePlaceholder = append(compiled.replacePlaceholder, compiledRepl)
			}
		}

		out = append(out, compiled)
	}

	return out, nil
}

func (r compiledRule) matchesRequest(req *http.Request) bool {
	host := normalizeHostForMatch(requestHost(req))
	if host == "" {
		return false
	}
	if _, ok := r.matchHosts[host]; !ok {
		return false
	}
	if len(r.matchPathPrefixes) == 0 {
		return true
	}
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	for _, pfx := range r.matchPathPrefixes {
		if strings.HasPrefix(path, pfx) {
			return true
		}
	}
	return false
}

func (r compiledRule) matchesConnectHost(host string) bool {
	host = normalizeHostForMatch(host)
	if host == "" {
		return false
	}
	_, ok := r.matchHosts[host]
	return ok
}

func (r compiledRule) apply(req *http.Request, secretValues map[string]string) error {
	for headerName, tmpl := range r.setHeader {
		value, err := renderSecretTemplate(tmpl, secretValues)
		if err != nil {
			return err
		}
		req.Header.Set(headerName, value)
	}
	for _, repl := range r.replacePlaceholder {
		applyCompiledPlaceholderReplacement(req, repl)
	}
	if r.setBasicAuthHeader != "" {
		req.Header.Set("Authorization", r.setBasicAuthHeader)
	}
	return nil
}

func applyCompiledPlaceholderReplacement(req *http.Request, repl compiledPlaceholderReplacement) {
	if req == nil || repl.placeholder == "" {
		return
	}

	if repl.inHeader {
		for headerName, values := range req.Header {
			changed := false
			for i, value := range values {
				replaced := strings.ReplaceAll(value, repl.placeholder, repl.secretValue)
				if replaced == value {
					continue
				}
				values[i] = replaced
				changed = true
			}
			if changed {
				req.Header[headerName] = values
			}
		}
	}

	if req.URL == nil {
		return
	}

	if repl.inQuery {
		query := req.URL.Query()
		changed := false
		for key, values := range query {
			for i, value := range values {
				replaced := strings.ReplaceAll(value, repl.placeholder, repl.secretValue)
				if replaced == value {
					continue
				}
				values[i] = replaced
				changed = true
			}
			query[key] = values
		}
		if changed {
			req.URL.RawQuery = query.Encode()
			req.RequestURI = req.URL.RequestURI()
		}
	}

	if repl.inPath {
		pathChanged := false
		replacedPath := strings.ReplaceAll(req.URL.Path, repl.placeholder, repl.secretValue)
		if replacedPath != req.URL.Path {
			req.URL.Path = replacedPath
			pathChanged = true
		}
		if req.URL.RawPath != "" {
			replacedRawPath := strings.ReplaceAll(
				req.URL.RawPath,
				repl.placeholder,
				repl.secretValue,
			)
			if replacedRawPath != req.URL.RawPath {
				req.URL.RawPath = replacedRawPath
				pathChanged = true
			}
		}
		if pathChanged {
			req.RequestURI = req.URL.RequestURI()
		}
	}
}

func normalizePathPrefixes(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

func requestHost(req *http.Request) string {
	if req == nil {
		return ""
	}
	if req.URL != nil && strings.TrimSpace(req.URL.Host) != "" {
		return req.URL.Host
	}
	return req.Host
}

func normalizeHostForMatch(raw string) string {
	host := strings.TrimSpace(raw)
	if host == "" {
		return ""
	}

	if h, p, err := net.SplitHostPort(host); err == nil {
		// Keep the host only for matching regardless of default/non-default ports.
		_ = p
		host = h
	}
	host = strings.Trim(host, "[]")
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(strings.TrimSpace(host))
}

package egressproxy

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

type Rule struct {
	Name              string
	MatchHosts        []string
	MatchPathPrefixes []string
	SetHeader         map[string]string
}

type compiledRule struct {
	name              string
	matchHosts        map[string]struct{}
	matchPathPrefixes []string
	setHeader         map[string]string
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
			for k, v := range rule.SetHeader {
				if _, err := renderSecretTemplate(v, secretValues); err != nil {
					return nil, fmt.Errorf("rule[%d].set_header[%q]: %w", i, k, err)
				}
				compiled.setHeader[k] = v
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

package stack

import (
	"fmt"

	"github.com/q15co/q15/libs/proxy-contract/policy"
	"go.yaml.in/yaml/v3"
)

// githubAPIRule is the one request the stock policy mutates: the agent's
// sandbox reaches GitHub's API with the token the stack already manages, and it
// reaches nothing else through the proxy because nothing else declares a rule.
const githubAPIRule = "github-api"

// ProxyPolicy renders the profile as the proxy's policy.
//
// The policy is the proxy's half of what a stack is: which secrets may be
// substituted, where they may be substituted, and which hosts are reached
// directly. Rendering it from the same profile as the units is what keeps the
// two in step - the service names a bypassed request names are the names the
// stack's network actually gives, so a renamed stack does not keep a policy
// pointing at containers that no longer exist.
func (p Profile) ProxyPolicy() (policy.Config, error) {
	if err := p.Validate(); err != nil {
		return policy.Config{}, err
	}

	cfg := policy.Config{
		SchemaVersion: policy.SchemaVersion,
		Proxy: policy.Proxy{
			// The stack's own services are reached directly. The proxy shares
			// the stack's network with everything it serves, so a request it
			// forwarded to a sibling would come back to itself.
			NoProxy: []string{
				"localhost",
				"127.0.0.1",
				"::1",
				p.resource("proxy"),
				p.resource("exec"),
			},
			SetLowercaseProxyEnv: true,
			Secrets:              []string{githubTokenFile},
			Rules: []policy.ProxyRule{
				{Name: githubAPIRule, MatchHosts: []string{"api.github.com"}},
			},
			Env: []policy.ProxyEnv{
				{
					Name:   "GH_TOKEN",
					Secret: githubTokenFile,
					Rules:  []string{githubAPIRule},
					In:     []string{"header"},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		return policy.Config{}, fmt.Errorf("stack: %w", err)
	}
	return cfg, nil
}

// ProxyPolicyYAML renders the validated policy as the YAML a stack consumes,
// the counterpart of AgentConfigYAML. Validation happens here rather than at the
// proxy's loader, so a mistake is a refused render and not a stack that starts
// and then exits.
func (p Profile) ProxyPolicyYAML() ([]byte, error) {
	cfg, err := p.ProxyPolicy()
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(cfg)
}

package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/egressproxy"
	"github.com/q15co/q15/systems/agent/internal/sandbox"
)

type startedSandboxProxy struct {
	server          *egressproxy.Server
	sandboxSettings *sandbox.ProxySettings
}

func startSandboxProxy(
	ctx context.Context,
	rtProxy *config.SandboxProxyRuntime,
) (*startedSandboxProxy, error) {
	if rtProxy == nil {
		return nil, nil
	}
	for i, rule := range rtProxy.Rules {
		if len(rule.ReplacePlaceholder) > 0 {
			return nil, fmt.Errorf(
				"proxy rule[%d] replace_placeholder is not implemented yet in embedded proxy",
				i,
			)
		}
	}

	server, err := egressproxy.Start(ctx, egressproxy.Config{
		ListenAddr:   rtProxy.ListenAddr,
		SecretValues: rtProxy.SecretValues,
		Rules:        toEgressProxyRules(rtProxy.Rules),
	})
	if err != nil {
		return nil, fmt.Errorf("start embedded egress proxy: %w", err)
	}

	proxyURL, err := server.ProxyURLForContainerHost(rtProxy.ContainerProxyHost)
	if err != nil {
		shutdownCtx, cancel := context.WithCancel(context.Background())
		_ = server.Stop(shutdownCtx)
		cancel()
		return nil, fmt.Errorf("build container proxy URL: %w", err)
	}
	caCertHostPath := strings.TrimSpace(server.CACertHostPath())
	caCertContainerPath := strings.TrimSpace(rtProxy.CACertContainerPath)
	if caCertHostPath != "" && caCertContainerPath == "" {
		shutdownCtx, cancel := context.WithCancel(context.Background())
		_ = server.Stop(shutdownCtx)
		cancel()
		return nil, fmt.Errorf("proxy CA cert container path is required when CA export is present")
	}

	go func() {
		if err, ok := <-server.Errors(); ok && err != nil {
			fmt.Fprintf(os.Stderr, "embedded egress proxy error: %v\n", err)
		}
	}()

	return &startedSandboxProxy{
		server: server,
		sandboxSettings: &sandbox.ProxySettings{
			Enabled:              true,
			HTTPProxyURL:         proxyURL,
			HTTPSProxyURL:        proxyURL,
			AllProxyURL:          proxyURL,
			NoProxy:              strings.Join(rtProxy.NoProxy, ","),
			SetLowercaseProxyEnv: rtProxy.SetLowercaseProxyEnv,
			CACertHostPath:       caCertHostPath,
			CACertContainerPath:  caCertContainerPath,
		},
	}, nil
}

func toEgressProxyRules(rules []config.SandboxProxyRule) []egressproxy.Rule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]egressproxy.Rule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, egressproxy.Rule{
			Name:              rule.Name,
			MatchHosts:        append([]string(nil), rule.MatchHosts...),
			MatchPathPrefixes: append([]string(nil), rule.MatchPathPrefixes...),
			SetHeader:         cloneStringMap(rule.SetHeader),
		})
	}
	return out
}

func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

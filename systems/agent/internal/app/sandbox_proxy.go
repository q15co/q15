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

	server, err := egressproxy.Start(ctx, egressproxy.Config{
		ListenAddr: rtProxy.ListenAddr,
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
			// MITM/CA trust wiring lands in the next slice. Leaving both CA paths
			// empty keeps sandbox/helper validation happy in passthrough mode.
			CACertHostPath:      "",
			CACertContainerPath: "",
		},
	}, nil
}

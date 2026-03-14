package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/q15co/q15/systems/proxy-service/internal/config"
	"github.com/q15co/q15/systems/proxy-service/internal/proxy"
	"github.com/q15co/q15/systems/proxy-service/internal/service"
	"google.golang.org/grpc"
)

const configEnvVar = "Q15_PROXY_SERVICE_CONFIG"

// Run parses CLI args and runs the requested command.
func Run(args []string) error {
	if len(args) == 0 {
		return runServe(nil)
	}
	switch strings.TrimSpace(args[0]) {
	case "", "serve":
		return runServe(args[1:])
	default:
		return fmt.Errorf("usage: q15-proxy-service [serve] --config <path>")
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	configPath := fs.String("config", envOrDefault(configEnvVar, ""), "proxy service config path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" {
		return errors.New("config path is required")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	runtime, err := config.LoadRuntime(*configPath)
	if err != nil {
		return err
	}
	return Serve(ctx, runtime)
}

// Serve starts the proxy data plane and admin control plane.
func Serve(ctx context.Context, runtime config.Runtime) error {
	dataPlane, err := proxy.Start(ctx, proxy.Config{
		ListenAddr:   runtime.ProxyListen,
		StateDir:     runtime.StateDir,
		SecretValues: runtime.SecretValues,
		Rules:        toProxyRules(runtime.Rules),
	})
	if err != nil {
		return err
	}

	adminAPI, err := service.NewGRPCServer(service.RuntimeInfo{
		ServiceVersion:       runtime.ServiceVersion,
		AdvertiseProxyURL:    runtime.AdvertiseProxyURL,
		NoProxy:              runtime.NoProxy,
		SetLowercaseProxyEnv: runtime.SetLowercaseProxyEnv,
		CACertPEM:            dataPlane.CACertPEM(),
		EnvValues:            runtime.EnvValues,
		PolicyRevision:       runtime.PolicyRevision,
	})
	if err != nil {
		shutdownCtx, cancel := context.WithCancel(context.Background())
		_ = dataPlane.Stop(shutdownCtx)
		cancel()
		return err
	}

	network, address := resolveListenTarget(runtime.AdminListen)
	if network == "unix" {
		_ = os.Remove(address)
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		shutdownCtx, cancel := context.WithCancel(context.Background())
		_ = dataPlane.Stop(shutdownCtx)
		cancel()
		return fmt.Errorf("listen %s %s: %w", network, address, err)
	}
	defer listener.Close()

	server := grpc.NewServer()
	adminAPI.Register(server)

	errCh := make(chan error, 2)
	go func() {
		errCh <- server.Serve(listener)
	}()
	go func() {
		if proxyErr, ok := <-dataPlane.Errors(); ok && proxyErr != nil {
			errCh <- proxyErr
		}
	}()

	select {
	case <-ctx.Done():
		server.GracefulStop()
		return dataPlane.Stop(context.Background())
	case err := <-errCh:
		server.Stop()
		shutdownCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_ = dataPlane.Stop(shutdownCtx)
		return err
	}
}

func envOrDefault(name string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func resolveListenTarget(value string) (string, string) {
	if strings.HasPrefix(value, "unix://") {
		return "unix", strings.TrimPrefix(value, "unix://")
	}
	return "tcp", value
}

func toProxyRules(rules []config.ProxyRule) []proxy.Rule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]proxy.Rule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, proxy.Rule{
			Name:               rule.Name,
			MatchHosts:         append([]string(nil), rule.MatchHosts...),
			MatchPathPrefixes:  append([]string(nil), rule.MatchPathPrefixes...),
			SetHeader:          cloneStringMap(rule.SetHeader),
			SetBasicAuth:       cloneBasicAuth(rule.SetBasicAuth),
			ReplacePlaceholder: clonePlaceholderReplacements(rule.ReplacePlaceholder),
		})
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

func cloneBasicAuth(value *config.ProxyBasicAuth) *proxy.BasicAuth {
	if value == nil {
		return nil
	}
	return &proxy.BasicAuth{
		Username: value.Username,
		Secret:   value.Secret,
	}
}

func clonePlaceholderReplacements(
	values []config.ProxyPlaceholderReplacement,
) []proxy.PlaceholderReplacement {
	if len(values) == 0 {
		return nil
	}
	out := make([]proxy.PlaceholderReplacement, 0, len(values))
	for _, value := range values {
		out = append(out, proxy.PlaceholderReplacement{
			Placeholder: value.Placeholder,
			Secret:      value.Secret,
			In:          append([]string(nil), value.In...),
		})
	}
	return out
}

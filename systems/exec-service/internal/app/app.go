// Package app wires the exec-service runtime into the gRPC server.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/q15co/q15/systems/exec-service/internal/service"
	"google.golang.org/grpc"
)

const (
	defaultListenAddr     = ":50051"
	defaultProxyAdminAddr = "q15-proxy-service:50052"
	defaultVersion        = "dev"
	runtimeWorkspaceDir   = "/workspace"
	runtimeMemoryDir      = "/memory"
	runtimeSkillsDir      = "/skills"
)

// Run validates args and starts the exec-service runtime.
func Run(args []string) error {
	if err := validateArgs(args); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return Serve(ctx, Settings{
		ListenAddr:        defaultListenAddr,
		ServiceVersion:    defaultVersion,
		ProxyAdminAddress: defaultProxyAdminAddr,
		Executor:          service.NixShellExecutor{},
	})
}

func validateArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	return errors.New("q15-exec-service accepts no arguments")
}

// Settings configures one gRPC server process.
type Settings struct {
	ListenAddr        string
	ServiceVersion    string
	ProxyAdminAddress string
	Executor          service.Executor
}

// Serve starts the gRPC service and blocks until the context is canceled.
func Serve(ctx context.Context, cfg Settings) error {
	cfg.ListenAddr = strings.TrimSpace(cfg.ListenAddr)
	if cfg.ListenAddr == "" {
		return errors.New("listen address is required")
	}
	if cfg.Executor == nil {
		cfg.Executor = service.NixShellExecutor{}
	}
	proxyProfile, cleanupProxy, err := bootstrapProxyRuntime(ctx, cfg.ProxyAdminAddress)
	if err != nil {
		return err
	}
	defer cleanupProxy()

	manager, err := service.NewManager(service.ManagerConfig{
		DefaultWorkingDir: runtimeWorkspaceDir,
		DefaultEnv:        proxyProfile.Env,
		Executor:          cfg.Executor,
	})
	if err != nil {
		return err
	}
	api, err := service.NewGRPCServer(manager, service.RuntimeInfo{
		ServiceVersion:      cfg.ServiceVersion,
		ExecutorType:        cfg.Executor.Type(),
		WorkspaceDir:        runtimeWorkspaceDir,
		MemoryDir:           runtimeMemoryDir,
		SkillsDir:           runtimeSkillsDir,
		ProxyEnabled:        proxyProfile.Enabled,
		ProxyPolicyRevision: proxyProfile.PolicyRevision,
	})
	if err != nil {
		return err
	}

	network, address := resolveListenTarget(cfg.ListenAddr)
	if network == "unix" {
		_ = os.Remove(address)
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return fmt.Errorf("listen %s %s: %w", network, address, err)
	}
	defer listener.Close()

	server := grpc.NewServer()
	api.Register(server)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		server.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}

func resolveListenTarget(value string) (string, string) {
	if strings.HasPrefix(value, "unix://") {
		return "unix", strings.TrimPrefix(value, "unix://")
	}
	return "tcp", value
}

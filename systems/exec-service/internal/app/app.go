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

	"github.com/q15co/q15/systems/exec-service/internal/service"
	"google.golang.org/grpc"
)

const (
	defaultListenAddr = "127.0.0.1:50051"
	defaultWorkspace  = "/workspace"
	defaultMemory     = "/memory"
	defaultSkills     = "/skills"
	defaultVersion    = "dev"
	listenEnvVar      = "Q15_EXEC_SERVICE_LISTEN"
	workspaceEnvVar   = "Q15_EXEC_SERVICE_WORKSPACE_DIR"
	memoryEnvVar      = "Q15_EXEC_SERVICE_MEMORY_DIR"
	skillsEnvVar      = "Q15_EXEC_SERVICE_SKILLS_DIR"
	versionEnvVar     = "Q15_EXEC_SERVICE_VERSION"
	proxyAdminEnvVar  = "Q15_EXEC_SERVICE_PROXY_ADMIN_ADDRESS"
)

// Run parses CLI args and runs the requested command.
func Run(args []string) error {
	if len(args) == 0 {
		return runServe(nil)
	}
	switch strings.TrimSpace(args[0]) {
	case "", "serve":
		return runServe(args[1:])
	default:
		return fmt.Errorf("usage: q15-exec-service [serve] [flags]")
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	listenAddr := fs.String(
		"listen",
		envOrDefault(listenEnvVar, defaultListenAddr),
		"listen address",
	)
	workspaceDir := fs.String(
		"workspace-dir",
		envOrDefault(workspaceEnvVar, defaultWorkspace),
		"default workspace dir",
	)
	memoryDir := fs.String("memory-dir", envOrDefault(memoryEnvVar, defaultMemory), "memory dir")
	skillsDir := fs.String("skills-dir", envOrDefault(skillsEnvVar, defaultSkills), "skills dir")
	serviceVersion := fs.String(
		"version",
		envOrDefault(versionEnvVar, defaultVersion),
		"service version label",
	)
	proxyAdminAddress := fs.String(
		"proxy-admin-address",
		envOrDefault(proxyAdminEnvVar, ""),
		"proxy admin service address",
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return Serve(ctx, Settings{
		ListenAddr:        *listenAddr,
		WorkspaceDir:      *workspaceDir,
		MemoryDir:         *memoryDir,
		SkillsDir:         *skillsDir,
		ServiceVersion:    *serviceVersion,
		ProxyAdminAddress: *proxyAdminAddress,
		Executor:          service.NixShellExecutor{},
	})
}

// Settings configures one gRPC server process.
type Settings struct {
	ListenAddr        string
	WorkspaceDir      string
	MemoryDir         string
	SkillsDir         string
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
	cfg.WorkspaceDir = strings.TrimSpace(cfg.WorkspaceDir)
	if cfg.WorkspaceDir == "" {
		return errors.New("workspace dir is required")
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
		DefaultWorkingDir: cfg.WorkspaceDir,
		DefaultEnv:        proxyProfile.Env,
		Executor:          cfg.Executor,
	})
	if err != nil {
		return err
	}
	api, err := service.NewGRPCServer(manager, service.RuntimeInfo{
		ServiceVersion:      cfg.ServiceVersion,
		ExecutorType:        cfg.Executor.Type(),
		WorkspaceDir:        cfg.WorkspaceDir,
		MemoryDir:           cfg.MemoryDir,
		SkillsDir:           cfg.SkillsDir,
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

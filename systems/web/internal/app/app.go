// Package app wires the web process, bridge handshake and graceful shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/q15co/q15/systems/web/internal/assets"
	"github.com/q15co/q15/systems/web/internal/auth"
	"github.com/q15co/q15/systems/web/internal/bridge"
	"github.com/q15co/q15/systems/web/internal/gate"
	"github.com/q15co/q15/systems/web/internal/server"
)

const bridgeConnectTimeout = 5 * time.Second

// Config supplies process paths and the fixed public origin.
type Config struct {
	Listen    string
	Bridge    string
	Origin    string
	StateDir  string
	Directory string
	TLSCert   string
	TLSKey    string
}

// Run validates args and starts the web tier. The supervisor retries startup
// when the agent is not ready; a protocol mismatch always fails hard.
func Run(args []string) error {
	if len(args) == 1 && args[0] == "--healthcheck" {
		return healthcheck(os.Getenv("Q15_WEB_LISTEN"))
	}
	stateDir := os.Getenv("Q15_WEB_STATE_DIR")
	if stateDir == "" {
		stateDir = "/var/lib/q15-web"
	}
	if len(args) > 0 && args[0] == "auth" {
		return auth.RunAdmin(stateDir, args[1:], os.Stdin, os.Stdout, os.Stderr)
	}
	if len(args) != 0 {
		return errors.New("q15-web accepts no arguments")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	config := Config{
		Listen:    os.Getenv("Q15_WEB_LISTEN"),
		Bridge:    os.Getenv("Q15_WEB_BRIDGE"),
		Origin:    os.Getenv("Q15_WEB_ORIGIN"),
		StateDir:  stateDir,
		Directory: os.Getenv("Q15_WEB_DIR"),
		TLSCert:   os.Getenv("Q15_WEB_TLS_CERT"),
		TLSKey:    os.Getenv("Q15_WEB_TLS_KEY"),
	}
	if config.Listen == "" {
		config.Listen = "127.0.0.1:8080"
	}
	if config.Bridge == "" {
		config.Bridge = "unix:///run/q15/bridge.sock"
	}
	return Serve(ctx, config)
}

// healthcheck is an HTTP probe usable in the shell-free runtime image. The
// listener opens only after policy, bundle and bridge handshake validation.
func healthcheck(listen string) error {
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if os.Getenv("Q15_WEB_TLS_CERT") != "" {
		scheme = "https"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(scheme + "://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("web health status: %d", response.StatusCode)
	}
	return nil
}

// Serve checks policy, assets and bridge compatibility before opening HTTP.
func Serve(ctx context.Context, config Config) error {
	if err := gate.ValidateOrigin(config.Origin); err != nil {
		return err
	}
	if (config.TLSCert == "") != (config.TLSKey == "") {
		return errors.New("both TLS certificate and key are required")
	}
	content, err := assets.Load(config.Directory)
	if err != nil {
		return err
	}
	defer content.Close()
	shell, err := content.Shell()
	if err != nil {
		return err
	}
	authorizer, err := auth.Open(config.StateDir, config.Origin, shell, nil)
	if err != nil {
		return err
	}
	defer authorizer.Close()
	client, err := connectBridge(ctx, config.Bridge)
	if err != nil {
		return err
	}
	defer client.Close()
	web, err := server.New(
		ctx,
		client,
		server.Config{Origin: config.Origin, Authorizer: authorizer, Assets: content},
	)
	if err != nil {
		return err
	}
	defer web.Close()
	adminListener, adminHandler, err := authorizer.ListenAdmin()
	if err != nil {
		return err
	}
	defer adminListener.Close()
	adminServer := &http.Server{Handler: adminHandler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 8192}
	defer adminServer.Close()
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	httpServer := &http.Server{
		Handler:           web,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    32 << 10,
	}
	defer httpServer.Close()
	errCh := make(chan error, 2)
	go func() { errCh <- adminServer.Serve(adminListener) }()
	go func() {
		if config.TLSCert != "" {
			errCh <- httpServer.ServeTLS(listener, config.TLSCert, config.TLSKey)
		} else {
			errCh <- httpServer.Serve(listener)
		}
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		web.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func connectBridge(ctx context.Context, target string) (*bridge.Client, error) {
	connectCtx, cancel := context.WithTimeout(ctx, bridgeConnectTimeout)
	defer cancel()
	client, err := bridge.NewClient(connectCtx, target)
	if err != nil {
		return nil, err
	}
	info, err := client.GetRuntimeInfo(connectCtx)
	if err == nil {
		err = bridge.CheckVersion(info)
	}
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("chat bridge handshake: %w", err)
	}
	return client, nil
}

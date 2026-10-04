// Command testserver serves the real owner gate for Playwright without an agent.
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/web/internal/bridge"
	"github.com/q15co/q15/systems/web/internal/server"
	"google.golang.org/grpc"

	"github.com/q15co/q15/systems/web/internal/assets"
	"github.com/q15co/q15/systems/web/internal/auth"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (err error) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	parent := os.Getenv("Q15_WEB_TEST_STATE_DIR")
	if !filepath.IsAbs(parent) {
		return errors.New("Q15_WEB_TEST_STATE_DIR must be an absolute test directory")
	}
	directory := filepath.Join(parent, "auth")
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	content, err := assets.Load("")
	if err != nil {
		return err
	}
	defer content.Close()
	shell, err := content.Shell()
	if err != nil {
		return err
	}
	audit, err := os.OpenFile(filepath.Join(parent, "web.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, audit.Close()) }()
	a, err := auth.Open(directory, "http://localhost:4184", shell,
		slog.New(slog.NewJSONHandler(audit, nil)))
	if err != nil {
		return err
	}
	defer a.Close()
	listener, adminHandler, err := a.ListenAdmin()
	if err != nil {
		return err
	}
	defer listener.Close()
	testService := newTestAgent()
	defer testService.content.Close()
	bridgePath := filepath.Join(directory, "bridge.sock")
	bridgeListener, err := net.Listen("unix", bridgePath)
	if err != nil {
		return err
	}
	defer bridgeListener.Close()
	rpc := grpc.NewServer()
	defer rpc.Stop()
	chatpb.RegisterChatServiceServer(rpc, testService)
	go func() { _ = rpc.Serve(bridgeListener) }()
	client, err := bridge.NewClient(ctx, "unix://"+bridgePath)
	if err != nil {
		return err
	}
	defer client.Close()
	chat, err := server.New(
		ctx,
		client,
		server.Config{Origin: "http://localhost:4184", Authorizer: a, Assets: content},
	)
	if err != nil {
		return err
	}
	defer chat.Close()
	public := &http.Server{
		Addr:              "127.0.0.1:4184",
		ReadHeaderTimeout: 5 * time.Second,
		Handler:           chat,
	}
	defer public.Close()
	admin := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: adminHandler}
	defer admin.Close()
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- admin.Serve(listener) }()
	go func() { errorsCh <- public.ListenAndServe() }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

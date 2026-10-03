// Command testserver serves the real owner gate for Playwright without an agent.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/q15co/q15/systems/web/internal/assets"
	"github.com/q15co/q15/systems/web/internal/auth"
	"github.com/q15co/q15/systems/web/internal/gate"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
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
	a, err := auth.Open(directory, "http://localhost:4184", shell, nil)
	if err != nil {
		return err
	}
	defer a.Close()
	listener, adminHandler, err := a.ListenAdmin()
	if err != nil {
		return err
	}
	defer listener.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/turns", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"turns":[],"head_seq":"0","has_more":false}`))
	})
	mux.Handle("/", content)
	secured := a.RequireScope("chat", mux)
	public := &http.Server{
		Addr:              "127.0.0.1:4184",
		ReadHeaderTimeout: 5 * time.Second,
		Handler: gate.Headers(
			"http://localhost:4184",
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthz" {
					w.WriteHeader(http.StatusOK)
					return
				}
				secured.ServeHTTP(w, r)
			}),
		),
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

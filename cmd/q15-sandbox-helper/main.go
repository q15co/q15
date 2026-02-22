package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"q15.co/sandbox/internal/sandbox"
	"q15.co/sandbox/internal/sandboxbuildah"
)

const helperRequestEnv = "Q15_SANDBOX_HELPER_REQUEST_B64"

func main() {
	if sandboxbuildah.InitProcess() {
		return
	}
	if err := sandboxbuildah.EnsureProcessEnvironment(); err != nil {
		writeResponse(sandbox.HelperResponse{Error: err.Error()})
		os.Exit(1)
	}
	if err := run(); err != nil {
		writeResponse(sandbox.HelperResponse{Error: err.Error()})
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: %s <prepare|exec>", os.Args[0])
	}
	action := os.Args[1]

	req, err := loadRequest()
	if err != nil {
		return err
	}

	cfg := sandboxbuildah.Settings{
		ContainerName:    req.Settings.ContainerName,
		FromImage:        req.Settings.FromImage,
		WorkspaceHostDir: req.Settings.WorkspaceHostDir,
		WorkspaceDir:     req.Settings.WorkspaceDir,
	}

	switch action {
	case "prepare":
		if err := sandboxbuildah.Prepare(context.Background(), cfg); err != nil {
			return err
		}
		writeResponse(sandbox.HelperResponse{})
		return nil
	case "exec":
		out, err := sandboxbuildah.Exec(context.Background(), cfg, req.Command)
		if err != nil {
			return err
		}
		writeResponse(sandbox.HelperResponse{Output: out})
		return nil
	default:
		return fmt.Errorf("unsupported action %q", action)
	}
}

func loadRequest() (sandbox.HelperRequest, error) {
	if encoded := os.Getenv(helperRequestEnv); encoded != "" {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return sandbox.HelperRequest{}, fmt.Errorf("decode %s: %w", helperRequestEnv, err)
		}
		var req sandbox.HelperRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return sandbox.HelperRequest{}, fmt.Errorf("decode helper request from %s: %w", helperRequestEnv, err)
		}
		return req, nil
	}

	var req sandbox.HelperRequest
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		if err == io.EOF {
			return sandbox.HelperRequest{}, fmt.Errorf("missing helper request JSON on stdin")
		}
		return sandbox.HelperRequest{}, fmt.Errorf("decode helper request: %w", err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return sandbox.HelperRequest{}, fmt.Errorf("re-encode helper request: %w", err)
	}
	if err := os.Setenv(helperRequestEnv, base64.StdEncoding.EncodeToString(raw)); err != nil {
		return sandbox.HelperRequest{}, fmt.Errorf("set %s: %w", helperRequestEnv, err)
	}
	return req, nil
}

func writeResponse(resp sandbox.HelperResponse) {
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(resp)
}

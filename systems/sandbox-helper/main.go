// Package main implements the Buildah helper subprocess used by q15 sandboxes.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"

	sandboxcontract "github.com/q15co/q15/libs/sandbox-contract"
	"github.com/q15co/q15/systems/sandbox-helper/internal/sandboxbuildah"
)

const helperRequestEnv = "Q15_SANDBOX_HELPER_REQUEST_B64"

func main() {
	if sandboxbuildah.InitProcess() {
		return
	}
	action, err := parseAction(os.Args)
	if err != nil {
		writeResponse(sandboxcontract.HelperResponse{Error: err.Error()})
		os.Exit(1)
	}
	if actionRequiresBuildahEnv(action) {
		if err := sandboxbuildah.EnsureProcessEnvironment(); err != nil {
			writeResponse(sandboxcontract.HelperResponse{Error: err.Error()})
			os.Exit(1)
		}
	}
	if err := run(action); err != nil {
		writeResponse(sandboxcontract.HelperResponse{Error: err.Error()})
		os.Exit(1)
	}
}

func parseAction(args []string) (string, error) {
	if len(args) != 2 {
		return "", fmt.Errorf("usage: %s <prepare|exec|metadata>", os.Args[0])
	}
	return args[1], nil
}

func actionRequiresBuildahEnv(action string) bool {
	switch action {
	case "prepare", "exec":
		return true
	case "metadata":
		return false
	default:
		return false
	}
}

func run(action string) error {
	switch action {
	case "metadata":
		metadata := sandboxbuildah.Metadata()
		writeResponse(sandboxcontract.HelperResponse{
			Metadata: &sandboxcontract.RuntimeMetadata{
				Runtime:   metadata.Runtime,
				BaseImage: metadata.BaseImage,
			},
		})
		return nil
	case "prepare", "exec":
	default:
		return fmt.Errorf("unsupported action %q", action)
	}

	req, err := loadRequest()
	if err != nil {
		return err
	}

	cfg := sandboxbuildah.Settings{
		ContainerName:    req.Settings.ContainerName,
		WorkspaceHostDir: req.Settings.WorkspaceHostDir,
		WorkspaceDir:     req.Settings.WorkspaceDir,
		MemoryHostDir:    req.Settings.MemoryHostDir,
		MemoryDir:        req.Settings.MemoryDir,
		Proxy:            toBuildahProxySettings(req.Settings.Proxy),
	}

	switch action {
	case "prepare":
		if err := sandboxbuildah.Prepare(context.Background(), cfg); err != nil {
			return err
		}
		writeResponse(sandboxcontract.HelperResponse{})
		return nil
	case "exec":
		out, err := sandboxbuildah.Exec(context.Background(), cfg, req.Command)
		if err != nil {
			return err
		}
		writeResponse(sandboxcontract.HelperResponse{Output: out})
		return nil
	default:
		return fmt.Errorf("unsupported action %q", action)
	}
}

func toBuildahProxySettings(proxy *sandboxcontract.ProxySettings) *sandboxbuildah.ProxySettings {
	if proxy == nil {
		return nil
	}
	return &sandboxbuildah.ProxySettings{
		Enabled:              proxy.Enabled,
		HTTPProxyURL:         proxy.HTTPProxyURL,
		HTTPSProxyURL:        proxy.HTTPSProxyURL,
		AllProxyURL:          proxy.AllProxyURL,
		NoProxy:              proxy.NoProxy,
		CACertHostPath:       proxy.CACertHostPath,
		CACertContainerPath:  proxy.CACertContainerPath,
		SetLowercaseProxyEnv: proxy.SetLowercaseProxyEnv,
		Env:                  maps.Clone(proxy.Env),
	}
}

func loadRequest() (sandboxcontract.HelperRequest, error) {
	if encoded := os.Getenv(helperRequestEnv); encoded != "" {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return sandboxcontract.HelperRequest{}, fmt.Errorf(
				"decode %s: %w",
				helperRequestEnv,
				err,
			)
		}
		var req sandboxcontract.HelperRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return sandboxcontract.HelperRequest{}, fmt.Errorf(
				"decode helper request from %s: %w",
				helperRequestEnv,
				err,
			)
		}
		return req, nil
	}

	var req sandboxcontract.HelperRequest
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		if err == io.EOF {
			return sandboxcontract.HelperRequest{}, fmt.Errorf(
				"missing helper request JSON on stdin",
			)
		}
		return sandboxcontract.HelperRequest{}, fmt.Errorf("decode helper request: %w", err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return sandboxcontract.HelperRequest{}, fmt.Errorf("re-encode helper request: %w", err)
	}
	if err := os.Setenv(helperRequestEnv, base64.StdEncoding.EncodeToString(raw)); err != nil {
		return sandboxcontract.HelperRequest{}, fmt.Errorf("set %s: %w", helperRequestEnv, err)
	}
	return req, nil
}

func writeResponse(resp sandboxcontract.HelperResponse) {
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(resp)
}

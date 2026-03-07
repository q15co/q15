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
	"github.com/q15co/q15/systems/sandbox-helper/internal/sandboxfiles"
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
		return "", fmt.Errorf(
			"usage: %s <prepare|exec-raw|exec-nix-shell-bash|read-file|write-file|edit-file|apply-patch|metadata>",
			os.Args[0],
		)
	}
	return args[1], nil
}

func actionRequiresBuildahEnv(action string) bool {
	switch action {
	case "prepare", "exec-raw", "exec-nix-shell-bash":
		return true
	case "metadata", "read-file", "write-file", "edit-file", "apply-patch":
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
	case "prepare",
		"exec-raw",
		"exec-nix-shell-bash",
		"read-file",
		"write-file",
		"edit-file",
		"apply-patch":
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
	fileCfg := sandboxfiles.Settings{
		WorkspaceHostDir: req.Settings.WorkspaceHostDir,
		WorkspaceDir:     req.Settings.WorkspaceDir,
		MemoryHostDir:    req.Settings.MemoryHostDir,
		MemoryDir:        req.Settings.MemoryDir,
	}

	switch action {
	case "prepare":
		if err := sandboxbuildah.Prepare(context.Background(), cfg); err != nil {
			return err
		}
		writeResponse(sandboxcontract.HelperResponse{})
		return nil
	case "exec-raw":
		out, err := sandboxbuildah.ExecRaw(context.Background(), cfg, req.Command)
		if err != nil {
			return err
		}
		writeResponse(sandboxcontract.HelperResponse{Output: out})
		return nil
	case "exec-nix-shell-bash":
		if req.ExecNixShellBash == nil {
			return fmt.Errorf("missing exec_nix_shell_bash request payload")
		}
		out, err := sandboxbuildah.ExecNixShellBash(
			context.Background(),
			cfg,
			sandboxbuildah.ExecNixShellBashRequest{
				Command:  req.ExecNixShellBash.Command,
				Packages: append([]string(nil), req.ExecNixShellBash.Packages...),
			},
		)
		if err != nil {
			return err
		}
		writeResponse(sandboxcontract.HelperResponse{Output: out})
		return nil
	case "read-file":
		if req.ReadFile == nil {
			return fmt.Errorf("missing read_file request payload")
		}
		result, err := sandboxfiles.ReadFile(fileCfg, *req.ReadFile)
		if err != nil {
			return err
		}
		writeResponse(sandboxcontract.HelperResponse{ReadFile: &result})
		return nil
	case "write-file":
		if req.WriteFile == nil {
			return fmt.Errorf("missing write_file request payload")
		}
		result, err := sandboxfiles.WriteFile(fileCfg, *req.WriteFile)
		if err != nil {
			return err
		}
		writeResponse(sandboxcontract.HelperResponse{WriteFile: &result})
		return nil
	case "edit-file":
		if req.EditFile == nil {
			return fmt.Errorf("missing edit_file request payload")
		}
		result, err := sandboxfiles.EditFile(fileCfg, *req.EditFile)
		if err != nil {
			return err
		}
		writeResponse(sandboxcontract.HelperResponse{EditFile: &result})
		return nil
	case "apply-patch":
		if req.ApplyPatch == nil {
			return fmt.Errorf("missing apply_patch request payload")
		}
		result, err := sandboxfiles.ApplyPatch(fileCfg, *req.ApplyPatch)
		if err != nil {
			return err
		}
		writeResponse(sandboxcontract.HelperResponse{ApplyPatch: &result})
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

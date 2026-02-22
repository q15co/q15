package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"q15.co/sandbox/internal/agent"
	"q15.co/sandbox/internal/bus"
	"q15.co/sandbox/internal/channel/telegram"
	"q15.co/sandbox/internal/config"
	"q15.co/sandbox/internal/provider/moonshot"
	"q15.co/sandbox/internal/sandbox"
	"q15.co/sandbox/internal/tools"
)

func runBot(ctx context.Context, rt config.AgentRuntime) error {
	token := strings.TrimSpace(rt.TelegramToken)
	if token == "" {
		return errors.New("telegram token is required")
	}
	apiKey := strings.TrimSpace(rt.ProviderAPIKey)
	if apiKey == "" {
		return errors.New("provider api key is required")
	}

	models := normalizeModelList(rt.Models)
	if len(models) == 0 {
		return errors.New("at least one model is required")
	}

	modelAdapter, err := newModelAdapter(rt)
	if err != nil {
		return err
	}

	agentSandbox := sandbox.New(sandbox.Settings{
		ContainerName:    rt.SandboxContainerName,
		FromImage:        rt.SandboxFromImage,
		WorkspaceHostDir: rt.WorkspaceHostDir,
		WorkspaceDir:     rt.WorkspaceDir,
		Network:          rt.SandboxNetwork,
	})
	if sandbox.VerboseEnabled() {
		fmt.Printf(
			"[app] preparing sandbox for agent=%q container=%q workspace_host_dir=%q workspace_dir=%q from_image=%q network=%q\n",
			rt.Name,
			rt.SandboxContainerName,
			rt.WorkspaceHostDir,
			rt.WorkspaceDir,
			rt.SandboxFromImage,
			rt.SandboxNetwork,
		)
	}
	if err := agentSandbox.Prepare(ctx); err != nil {
		return fmt.Errorf("prepare sandbox for agent %q: %w", rt.Name, err)
	}
	if sandbox.VerboseEnabled() {
		fmt.Printf(
			"[app] sandbox ready for agent=%q container=%q\n",
			rt.Name,
			rt.SandboxContainerName,
		)
	}

	systemPrompt := agent.DefaultSystemPrompt
	if info, err := agentSandbox.Describe(ctx); err != nil {
		if sandbox.VerboseEnabled() {
			fmt.Fprintf(os.Stderr, "[app] sandbox probe failed for agent=%q: %v\n", rt.Name, err)
		}
		systemPrompt = composeSystemPrompt(systemPrompt, sandbox.SandboxInfo{
			ContainerName:    rt.SandboxContainerName,
			FromImage:        rt.SandboxFromImage,
			WorkspaceHostDir: rt.WorkspaceHostDir,
			WorkspaceDir:     rt.WorkspaceDir,
			Network:          rt.SandboxNetwork,
		})
	} else {
		systemPrompt = composeSystemPrompt(systemPrompt, info)
	}

	toolRunner := tools.NewShell(agentSandbox)
	messageBus := bus.New(bus.DefaultBufferSize)

	var (
		mu     sync.Mutex
		agents = make(map[string]agent.Agent)
	)

	getAgent := func(sessionKey string) agent.Agent {
		mu.Lock()
		defer mu.Unlock()

		if a, ok := agents[sessionKey]; ok {
			return a
		}

		a := agent.NewLoop(modelAdapter, toolRunner, models, systemPrompt)
		agents[sessionKey] = a
		return a
	}

	channel, err := telegram.NewChannel(token, func(msg telegram.IncomingMessage) {
		err := messageBus.PublishInbound(ctx, bus.InboundMessage{
			Channel: bus.ChannelTelegram,
			ChatID:  msg.ChatID,
			UserID:  msg.UserID,
			Text:    msg.Text,
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "publish inbound error: %v\n", err)
		}
	}, telegram.WithAllowedUserIDs(rt.TelegramAllowedUserIDs))
	if err != nil {
		return err
	}
	if err := channel.Start(ctx); err != nil {
		return err
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- runAgentWorker(ctx, messageBus, getAgent)
	}()
	go func() {
		errCh <- runOutboundWorker(ctx, messageBus, bus.ChannelTelegram, channel.SendText)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
}

func composeSystemPrompt(base string, info sandbox.SandboxInfo) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = agent.DefaultSystemPrompt
	}

	var lines []string
	lines = append(lines, "Sandbox Environment (authoritative runtime info):")
	if info.OSPrettyName != "" {
		lines = append(lines, fmt.Sprintf("- OS: %s", info.OSPrettyName))
	} else if info.OSID != "" || info.OSVersionID != "" {
		lines = append(lines, fmt.Sprintf("- OS: %s %s", strings.TrimSpace(info.OSID), strings.TrimSpace(info.OSVersionID)))
	}
	if info.FromImage != "" {
		lines = append(lines, fmt.Sprintf("- Base image: %s", info.FromImage))
	}
	if info.ContainerName != "" {
		lines = append(
			lines,
			fmt.Sprintf("- Sandbox container: %s (persistent Buildah builder)", info.ContainerName),
		)
	}
	if info.WorkspaceDir != "" {
		lines = append(
			lines,
			fmt.Sprintf(
				"- Workspace: %s (bind-mounted persistent host directory)",
				info.WorkspaceDir,
			),
		)
	}
	if info.Network != "" {
		lines = append(lines, fmt.Sprintf("- Network: %s", info.Network))
		if info.Network == "disabled" {
			lines = append(
				lines,
				"- Note: internet access from inside this sandbox is disabled; package installs/downloads will fail unless artifacts are already present locally.",
			)
		}
	}
	if info.PackageManager != "" {
		lines = append(lines, fmt.Sprintf("- Package manager: %s", info.PackageManager))
		lines = append(
			lines,
			fmt.Sprintf(
				"- Prefer using `%s` for installing tools in this sandbox.",
				packageManagerInstallHint(info.PackageManager),
			),
		)
		if info.PackageManager == "apt-get" {
			lines = append(
				lines,
				"- In this rootless sandbox, Debian/Ubuntu package downloads may fail when apt tries to drop privileges to `_apt`.",
			)
			lines = append(
				lines,
				"- If that happens, use: `apt-get -o APT::Sandbox::User=root update && DEBIAN_FRONTEND=noninteractive apt-get -o APT::Sandbox::User=root install -y <package>`",
			)
		}
	}
	if len(info.ShellsAvailable) > 0 {
		lines = append(
			lines,
			fmt.Sprintf("- Available shells: %s", strings.Join(info.ShellsAvailable, ", ")),
		)
	}
	if len(info.ToolsAvailable) > 0 {
		lines = append(
			lines,
			fmt.Sprintf(
				"- Preinstalled tools detected: %s",
				strings.Join(info.ToolsAvailable, ", "),
			),
		)
	}

	return base + "\n\n" + strings.Join(lines, "\n")
}

func packageManagerInstallHint(pm string) string {
	switch strings.TrimSpace(pm) {
	case "apt-get":
		return "apt-get update && apt-get install -y <package>"
	case "apk":
		return "apk add <package>"
	case "pacman":
		return "pacman -S --noconfirm <package>"
	case "dnf":
		return "dnf install -y <package>"
	case "microdnf":
		return "microdnf install -y <package>"
	case "yum":
		return "yum install -y <package>"
	case "zypper":
		return "zypper --non-interactive install <package>"
	case "nix":
		return "nix profile install <package>"
	default:
		return pm + " <package>"
	}
}

func newModelAdapter(rt config.AgentRuntime) (agent.Model, error) {
	switch strings.ToLower(strings.TrimSpace(rt.ProviderType)) {
	case "openai-compatible":
		return moonshot.NewClient(rt.ProviderBaseURL, rt.ProviderAPIKey), nil
	default:
		return nil, fmt.Errorf("unsupported provider type %q", rt.ProviderType)
	}
}

func normalizeModelList(models []string) []string {
	if len(models) == 0 {
		return nil
	}

	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

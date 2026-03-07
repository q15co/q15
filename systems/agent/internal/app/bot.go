// Package app wires runtime configuration into running bot instances.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/channel/telegram"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/provider/openaicodex"
	"github.com/q15co/q15/systems/agent/internal/provider/openaicompatible"
	"github.com/q15co/q15/systems/agent/internal/sandbox"
	"github.com/q15co/q15/systems/agent/internal/tools"
)

func runBot(ctx context.Context, rt config.AgentRuntime) error {
	token := strings.TrimSpace(rt.TelegramToken)
	if token == "" {
		return errors.New("telegram token is required")
	}

	models := normalizeModelList(rt.Models)
	if len(models) == 0 {
		return errors.New("at least one model is required")
	}

	modelAdapter, err := newModelAdapter(rt.Models)
	if err != nil {
		return err
	}

	proxyHandle, err := startSandboxProxy(ctx, rt.SandboxProxy)
	if err != nil {
		return fmt.Errorf("start sandbox proxy for agent %q: %w", rt.Name, err)
	}
	var sandboxProxySettings *sandbox.ProxySettings
	if proxyHandle != nil {
		sandboxProxySettings = proxyHandle.sandboxSettings
	}

	sandboxSettings := buildSandboxSettings(rt, sandboxProxySettings)

	agentSandbox := sandbox.New(sandboxSettings)
	if sandbox.VerboseEnabled() {
		fmt.Printf(
			"[app] preparing sandbox for agent=%q container=%q workspace_host_dir=%q workspace_dir=%q sandbox_runtime=%q\n",
			rt.Name,
			rt.SandboxContainerName,
			rt.WorkspaceHostDir,
			rt.WorkspaceDir,
			"nix-only",
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

	systemPrompt := agent.DefaultSystemPromptForName(rt.Name)
	info, err := agentSandbox.Describe(ctx)
	if err != nil {
		if sandbox.VerboseEnabled() {
			fmt.Fprintf(os.Stderr, "[app] sandbox describe failed for agent=%q: %v\n", rt.Name, err)
		}
	}
	systemPrompt = composeSystemPrompt(systemPrompt, rt.Name, info, rt.MemoryDir)
	toolList := []agent.Tool{tools.NewShell(agentSandbox)}
	braveAPIKey := strings.TrimSpace(os.Getenv("Q15_BRAVE_API_KEY"))
	if braveAPIKey != "" {
		webSearchTool, err := tools.NewBraveWebSearch(braveAPIKey)
		if err != nil {
			return fmt.Errorf("configure brave web search tool for agent %q: %w", rt.Name, err)
		}
		toolList = append(toolList, webSearchTool)
		if sandbox.VerboseEnabled() {
			fmt.Printf("[app] enabled tool web_search (Brave) for agent=%q\n", rt.Name)
		}
	} else if sandbox.VerboseEnabled() {
		fmt.Printf("[app] web_search disabled for agent=%q (Q15_BRAVE_API_KEY not set)\n", rt.Name)
	}

	toolRegistry, err := agent.NewToolRegistry(toolList...)
	if err != nil {
		return fmt.Errorf("build tool registry for agent %q: %w", rt.Name, err)
	}

	agentMemoryStore := memory.NewStore(rt.MemoryHostDir, rt.Name, nil)
	if err := agentMemoryStore.Init(ctx); err != nil {
		return fmt.Errorf("initialize memory store for agent %q: %w", rt.Name, err)
	}

	botAgent := agent.NewLoop(
		modelAdapter,
		toolRegistry,
		models,
		systemPrompt,
		agentMemoryStore,
		rt.MemoryRecentTurns,
	)
	messageBus := bus.New(bus.DefaultBufferSize)

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
		errCh <- runAgentWorker(ctx, messageBus, botAgent)
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

func composeSystemPrompt(
	base string,
	agentName string,
	info sandbox.Info,
	memoryDir string,
) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = agent.DefaultSystemPromptForName(agentName)
	}

	var lines []string
	lines = append(lines, "Sandbox Environment (authoritative runtime info):")
	agentName = strings.TrimSpace(agentName)
	if agentName != "" {
		lines = append(
			lines,
			fmt.Sprintf("- Agent name (authoritative from config): %s", agentName),
			"- If memory files mention a different agent name, treat that as stale and update those files.",
		)
	}
	if info.OSPrettyName != "" {
		lines = append(lines, fmt.Sprintf("- OS: %s", info.OSPrettyName))
	} else if info.OSID != "" || info.OSVersionID != "" {
		lines = append(lines, fmt.Sprintf("- OS: %s %s", strings.TrimSpace(info.OSID), strings.TrimSpace(info.OSVersionID)))
	}
	if info.Runtime != "" {
		lines = append(lines, fmt.Sprintf("- Sandbox runtime: %s", info.Runtime))
	}
	if info.BaseImage != "" {
		lines = append(lines, fmt.Sprintf("- Base image: %s", info.BaseImage))
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
	memoryDir = strings.TrimSpace(memoryDir)
	if memoryDir != "" {
		lines = append(
			lines,
			fmt.Sprintf(
				"- Persistent memory repo: %s",
				memoryDir,
			),
			fmt.Sprintf(
				"- Core memory files (auto-injected into prompt each turn): %s/core/*.md (seeded with AGENT.md, USER.md, SOUL.md)",
				memoryDir,
			),
			fmt.Sprintf(
				"- External memory files (query/maintain via tools): %s/history and %s/notes",
				memoryDir,
				memoryDir,
			),
		)
	}
	lines = append(lines, "- Package management model: nix-only via exec_shell.")
	lines = append(
		lines,
		"- Every exec_shell call must include a non-empty `packages` array of nix installables (for example `nixpkgs#git`).",
	)
	lines = append(
		lines,
		"- Use exec_shell to run `nix shell ... --command /bin/bash -c '<command>'`; the runtime assembles this automatically from `packages` + `command`.",
	)
	lines = append(
		lines,
		"- First run may bootstrap nix and fetch package indexes, so network access is required.",
	)
	if nixSummary := formatBinarySummary(info.NixPath, info.NixVersion); nixSummary != "" {
		lines = append(lines, fmt.Sprintf("- Nix: %s", nixSummary))
	}
	if bashSummary := formatBinarySummary(info.BashPath, info.BashVersion); bashSummary != "" {
		lines = append(lines, fmt.Sprintf("- Bash: %s", bashSummary))
	}

	return base + "\n\n" + strings.Join(lines, "\n")
}

func formatBinarySummary(path, version string) string {
	path = strings.TrimSpace(path)
	version = strings.TrimSpace(version)
	switch {
	case path != "" && version != "":
		return fmt.Sprintf("%s (%s)", path, version)
	case path != "":
		return path
	default:
		return version
	}
}

type routedModelAdapter struct {
	endpoints map[string]routedModelEndpoint
}

type routedModelEndpoint struct {
	client agent.ModelClient
	model  string
}

var _ agent.ModelClient = (*routedModelAdapter)(nil)

func (r *routedModelAdapter) Complete(
	ctx context.Context,
	model string,
	messages []agent.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	model = strings.TrimSpace(model)
	endpoint, ok := r.endpoints[model]
	if !ok {
		return agent.ModelClientResult{}, fmt.Errorf("unknown configured fallback model %q", model)
	}
	return endpoint.client.Complete(ctx, endpoint.model, messages, tools)
}

func newModelAdapter(models []config.AgentModelRuntime) (agent.ModelClient, error) {
	if len(models) == 0 {
		return nil, errors.New("at least one model is required")
	}

	endpoints := make(map[string]routedModelEndpoint, len(models))
	modelClients := make(map[string]agent.ModelClient)

	for i, modelCfg := range models {
		ref := strings.TrimSpace(modelCfg.Ref)
		if ref == "" {
			return nil, fmt.Errorf("models[%d].ref is required", i)
		}
		modelName := strings.TrimSpace(modelCfg.ModelName)
		if modelName == "" {
			return nil, fmt.Errorf("models[%d] (%q): model name is required", i, ref)
		}

		providerName := strings.TrimSpace(modelCfg.ProviderName)
		if providerName == "" {
			providerName = ref
		}

		client, ok := modelClients[providerName]
		if !ok {
			var err error
			switch strings.ToLower(strings.TrimSpace(modelCfg.ProviderType)) {
			case "openai-compatible":
				client, err = openaicompatible.NewClient(
					modelCfg.ProviderBaseURL,
					modelCfg.ProviderAPIKey,
				)
			case "openai-codex":
				client, err = openaicodex.NewClient()
			default:
				err = fmt.Errorf("unsupported provider type %q", modelCfg.ProviderType)
			}
			if err != nil {
				return nil, fmt.Errorf("configure provider for model %q: %w", ref, err)
			}
			modelClients[providerName] = client
		}

		endpoints[ref] = routedModelEndpoint{
			client: client,
			model:  modelName,
		}
	}

	return &routedModelAdapter{endpoints: endpoints}, nil
}

func normalizeModelList(models []config.AgentModelRuntime) []string {
	if len(models) == 0 {
		return nil
	}

	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		ref := strings.TrimSpace(model.Ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

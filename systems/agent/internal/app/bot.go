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
	"github.com/q15co/q15/systems/agent/internal/execution"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/provider/openaicodex"
	"github.com/q15co/q15/systems/agent/internal/provider/openaicompatible"
	"github.com/q15co/q15/systems/agent/internal/sandbox"
	q15skills "github.com/q15co/q15/systems/agent/internal/skills"
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

	sandboxSettings := buildSandboxSettings(rt)
	executionClient, executionInfo, err := connectExecutionService(ctx, rt.Execution)
	if err != nil {
		return fmt.Errorf("connect execution service for agent %q: %w", rt.Name, err)
	}
	if executionClient != nil {
		defer executionClient.Close()
	}

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

	skillManager := q15skills.NewManager(q15skills.Settings{
		WorkspaceHostDir: rt.WorkspaceHostDir,
		WorkspaceDir:     rt.WorkspaceDir,
		SkillsHostDir:    rt.SkillsHostDir,
		SkillsDir:        rt.SkillsDir,
	})
	braveAPIKey := strings.TrimSpace(os.Getenv("Q15_BRAVE_API_KEY"))
	toolList, err := buildToolList(agentSandbox, executionClient, skillManager, braveAPIKey)
	if err != nil {
		return fmt.Errorf("configure tools for agent %q: %w", rt.Name, err)
	}
	if sandbox.VerboseEnabled() {
		fmt.Printf(
			"[app] enabled tools read_file, write_file, edit_file, apply_patch for agent=%q\n",
			rt.Name,
		)
	}
	if sandbox.VerboseEnabled() {
		fmt.Printf("[app] enabled tool web_fetch for agent=%q\n", rt.Name)
	}
	if executionInfo != nil && sandbox.VerboseEnabled() {
		fmt.Printf(
			"[app] enabled tool exec for agent=%q via execution service=%q executor=%q\n",
			rt.Name,
			rt.Execution.ServiceAddress,
			executionInfo.GetExecutorType(),
		)
	}
	if braveAPIKey != "" {
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

	systemPrompt := agent.DefaultSystemPromptForName(rt.Name)
	info, err := agentSandbox.Describe(ctx)
	if err != nil {
		if sandbox.VerboseEnabled() {
			fmt.Fprintf(os.Stderr, "[app] sandbox describe failed for agent=%q: %v\n", rt.Name, err)
		}
	}
	systemPrompt = composeSystemPrompt(
		systemPrompt,
		rt.Name,
		info,
		rt.MemoryDir,
		toolRegistry.Definitions(),
	)

	memoryStore := memory.NewStore(rt.MemoryHostDir, rt.Name, nil)
	if err := memoryStore.Init(ctx); err != nil {
		return fmt.Errorf("initialize memory store for agent %q: %w", rt.Name, err)
	}
	store := &runtimeStore{
		memory: memoryStore,
		skills: skillManager,
	}

	botAgent := agent.NewLoop(
		modelAdapter,
		toolRegistry,
		models,
		systemPrompt,
		store,
		rt.MemoryRecentTurns,
	)
	messageBus := bus.New(bus.DefaultBufferSize)

	channel, err := telegram.NewChannel(token, func(msg telegram.IncomingMessage) {
		err := messageBus.PublishInbound(ctx, bus.InboundMessage{
			Channel:   bus.ChannelTelegram,
			ChatID:    msg.ChatID,
			UserID:    msg.UserID,
			MessageID: msg.MessageID,
			Text:      msg.Text,
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

	telegramEndpoint := telegram.NewAgentEndpoint(channel)
	errCh := make(chan error, 1)
	go func() {
		errCh <- runAgentWorker(ctx, messageBus, botAgent, telegramEndpoint)
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

func buildToolList(
	agentSandbox *sandbox.Sandbox,
	execClient execution.Service,
	skillManager *q15skills.Manager,
	braveAPIKey string,
) ([]agent.Tool, error) {
	fileExec := q15skills.NewFileExecutor(agentSandbox, skillManager)
	toolList := []agent.Tool{
		tools.NewReadFile(fileExec),
		tools.NewWriteFile(fileExec),
		tools.NewEditFile(fileExec),
		tools.NewApplyPatch(fileExec),
		tools.NewValidateSkill(skillManager),
	}
	if execClient != nil {
		toolList = append(toolList, tools.NewExec(execClient))
	}
	toolList = append(
		toolList,
		tools.NewNixShellBash(agentSandbox),
		tools.NewBrowserShell(agentSandbox),
		tools.NewWebFetch(),
	)

	braveAPIKey = strings.TrimSpace(braveAPIKey)
	if braveAPIKey == "" {
		return toolList, nil
	}

	webSearchTool, err := tools.NewBraveWebSearch(braveAPIKey)
	if err != nil {
		return nil, err
	}
	return append(toolList, webSearchTool), nil
}

func composeSystemPrompt(
	base string,
	agentName string,
	info sandbox.Info,
	memoryDir string,
	toolDefs []agent.ToolDefinition,
) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = agent.DefaultSystemPromptForName(agentName)
	}

	parts := []string{base}
	if sandboxSection := renderSandboxEnvironmentPrompt(agentName, info, memoryDir, toolDefs); sandboxSection != "" {
		parts = append(parts, sandboxSection)
	}
	if toolAdviceSection := renderToolAdvicePrompt(toolDefs); toolAdviceSection != "" {
		parts = append(parts, toolAdviceSection)
	}
	return strings.Join(parts, "\n\n")
}

func renderSandboxEnvironmentPrompt(
	agentName string,
	info sandbox.Info,
	memoryDir string,
	toolDefs []agent.ToolDefinition,
) string {
	var lines []string
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
	if info.SkillsDir != "" {
		lines = append(
			lines,
			fmt.Sprintf(
				"- Shared skills root: %s (bind-mounted when skills.host_dir is configured)",
				info.SkillsDir,
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
	if hasToolNamed(toolDefs, "exec") {
		lines = append(
			lines,
			"- Package management model: nix-only via exec and exec_browser_shell; exec_nix_shell_bash remains available as a legacy compatibility path.",
		)
	} else {
		lines = append(
			lines,
			"- Package management model: nix-only via exec_nix_shell_bash and exec_browser_shell.",
		)
	}
	lines = append(
		lines,
		"- Built-in skills are available read-only under `/skills/@builtin/...` via read_file even when no shared skills mount is configured.",
		"- Shared skills, when configured, are available under `/skills/<name>/...` and may be edited with the normal file tools.",
		"- Use read_file for routine UTF-8 text reads from the workspace, memory, or skills roots; paths may be relative to the workspace or absolute under `/workspace/...`, `/memory/...`, `/skills/...`, or `/skills/@builtin/...`.",
		"- Use write_file to create or fully replace UTF-8 text files in the workspace, memory, or shared skills roots.",
		"- Use edit_file for a single exact text replacement in an existing UTF-8 text file in the workspace, memory, or shared skills roots when you know the current text.",
		"- Use apply_patch for multi-file or diff-style edits in the workspace, memory, or shared skills roots using the high-level patch envelope.",
		"- Use validate_skill after creating or updating a skill directory.",
		"- apply_patch does not accept unified diff, git diff, or context diff syntax. Never send `diff --git`, `--- a/...`, `+++ b/...`, `*** a/...`, `*** b/...`, or bare path lines.",
		"- apply_patch patches must start with `*** Begin Patch` and end with `*** End Patch`.",
		"- Inside apply_patch, use exactly one of `*** Add File: PATH`, `*** Delete File: PATH`, or `*** Update File: PATH`. For renames, put `*** Move to: NEW_PATH` immediately after `*** Update File: PATH`.",
		"- In `*** Add File`, every file-content line must start with `+`.",
		"- In `*** Update File`, each hunk must start with `@@`, then use a leading space for context lines, `-` for removed lines, and `+` for added lines.",
		"- Minimal apply_patch example:",
		"```text\n*** Begin Patch\n*** Update File: /memory/notes/todo.md\n@@\n unchanged line\n-old value\n+new value\n unchanged tail\n*** End Patch\n```",
	)
	if hasToolNamed(toolDefs, "exec") {
		lines = append(
			lines,
			"- Prefer exec for commands, builds, tests, formatting, git, and other CLI workflows, not for routine file reads or edits.",
			"- Every exec call must include a non-empty `packages` array of nix installables (for example `nixpkgs#git`).",
			"- Use exec by providing the user command in `command` and the needed nix installables in `packages`; the execution service starts a session, streams stdout/stderr internally, and returns when the command exits.",
			"- Use exec for proxy-authenticated CLI flows such as `gh`, `git`, or `curl` when q15 is deployed with a separate proxy-service.",
			"- exec_nix_shell_bash remains available for legacy compatibility; prefer exec unless you specifically need the older direct sandbox-helper path.",
		)
	} else {
		lines = append(
			lines,
			"- Use exec_nix_shell_bash for commands, builds, tests, formatting, git, and other CLI workflows, not for routine file reads or edits.",
			"- Every exec_nix_shell_bash call must include a non-empty `packages` array of nix installables (for example `nixpkgs#git`).",
			"- Use exec_nix_shell_bash by providing the user command in `command` and the needed nix installables in `packages`; the sandbox runtime provisions those packages and executes the command via nix shell and bash.",
		)
	}
	lines = append(
		lines,
		"- First run may bootstrap nix and fetch package indexes, so network access is required.",
	)
	lines = append(
		lines,
		"- Use exec_browser_shell for browser automation, screenshots, scraping, Playwright, Puppeteer, and browser tests.",
		"- exec_browser_shell provisions the browser-ready nix package set automatically; use `display_mode` `headless` by default and switch to `xvfb` only for headed browser commands that still terminate on their own.",
		"- exec_browser_shell waits for the command to exit before returning; avoid long-running interactive commands such as `playwright open` or `playwright codegen`.",
		"- Use the nixpkgs-provided `playwright` and `puppeteer` wrappers in exec_browser_shell, and do not rely on `playwright install` or `playwright install-deps` inside the sandbox.",
	)
	lines = append(
		lines,
		"- Use web_fetch for known web page URLs: it returns cleaned markdown plus slice metadata and is preferred over using exec or exec_nix_shell_bash with curl for ordinary webpage reads.",
		"- Use web_search for discovering current sources, then use web_fetch on a chosen result URL when you need page contents.",
	)
	if nixSummary := formatBinarySummary(info.NixPath, info.NixVersion); nixSummary != "" {
		lines = append(lines, fmt.Sprintf("- Nix: %s", nixSummary))
	}
	if bashSummary := formatBinarySummary(info.BashPath, info.BashVersion); bashSummary != "" {
		lines = append(lines, fmt.Sprintf("- Bash: %s", bashSummary))
	}
	if len(lines) == 0 {
		return ""
	}

	return agent.RenderPromptElement("sandbox_environment", nil, strings.Join(lines, "\n"))
}

func hasToolNamed(toolDefs []agent.ToolDefinition, name string) bool {
	name = strings.TrimSpace(name)
	for _, tool := range toolDefs {
		if strings.TrimSpace(tool.Name) == name {
			return true
		}
	}
	return false
}

func renderToolAdvicePrompt(toolDefs []agent.ToolDefinition) string {
	if len(toolDefs) == 0 {
		return ""
	}

	renderedTools := make([]string, 0, len(toolDefs))
	for _, tool := range toolDefs {
		name := strings.TrimSpace(tool.Name)
		if name == "" || len(tool.PromptGuidance) == 0 {
			continue
		}

		lines := make([]string, 0, len(tool.PromptGuidance))
		seen := make(map[string]struct{}, len(tool.PromptGuidance))
		for _, line := range tool.PromptGuidance {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if _, ok := seen[line]; ok {
				continue
			}
			seen[line] = struct{}{}
			lines = append(lines, "- "+line)
		}
		if len(lines) == 0 {
			continue
		}

		attrs := map[string]string{"name": name}
		if desc := strings.TrimSpace(tool.Description); desc != "" {
			attrs["summary"] = desc
		}
		rendered := agent.RenderPromptElement("tool", attrs, strings.Join(lines, "\n"))
		if rendered == "" {
			continue
		}
		renderedTools = append(renderedTools, rendered)
	}
	if len(renderedTools) == 0 {
		return ""
	}

	return agent.RenderPromptElement("tool_advice", nil, strings.Join(renderedTools, "\n\n"))
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
	client        agent.ModelClient
	providerModel string
	capabilities  config.ModelCapabilities
}

type modelClientFactory func(config.AgentModelRuntime) (agent.ModelClient, error)

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

	if !endpoint.capabilities.ToolCalling {
		tools = nil
	}

	return endpoint.client.Complete(ctx, endpoint.providerModel, messages, tools)
}

func newModelAdapter(models []config.AgentModelRuntime) (agent.ModelClient, error) {
	return newModelAdapterWithFactory(models, defaultModelClientFactory)
}

func newModelAdapterWithFactory(
	models []config.AgentModelRuntime,
	clientFactory modelClientFactory,
) (agent.ModelClient, error) {
	if len(models) == 0 {
		return nil, errors.New("at least one model is required")
	}
	if clientFactory == nil {
		return nil, errors.New("model client factory is required")
	}

	endpoints := make(map[string]routedModelEndpoint, len(models))
	modelClients := make(map[string]agent.ModelClient)

	for i, modelCfg := range models {
		ref := strings.TrimSpace(modelCfg.Ref)
		if ref == "" {
			return nil, fmt.Errorf("models[%d].ref is required", i)
		}
		providerModel := strings.TrimSpace(modelCfg.ProviderModel)
		if providerModel == "" {
			return nil, fmt.Errorf("models[%d] (%q): provider model is required", i, ref)
		}

		providerName := strings.TrimSpace(modelCfg.ProviderName)
		if providerName == "" {
			providerName = ref
		}

		client, ok := modelClients[providerName]
		if !ok {
			var err error
			client, err = clientFactory(modelCfg)
			if err != nil {
				return nil, fmt.Errorf("configure provider for model %q: %w", ref, err)
			}
			modelClients[providerName] = client
		}

		endpoints[ref] = routedModelEndpoint{
			client:        client,
			providerModel: providerModel,
			capabilities:  modelCfg.Capabilities,
		}
	}

	return &routedModelAdapter{endpoints: endpoints}, nil
}

func defaultModelClientFactory(modelCfg config.AgentModelRuntime) (agent.ModelClient, error) {
	switch strings.ToLower(strings.TrimSpace(modelCfg.ProviderType)) {
	case "openai-compatible":
		return openaicompatible.NewClient(
			modelCfg.ProviderBaseURL,
			modelCfg.ProviderAPIKey,
		)
	case "openai-codex":
		return openaicodex.NewClient()
	default:
		return nil, fmt.Errorf("unsupported provider type %q", modelCfg.ProviderType)
	}
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

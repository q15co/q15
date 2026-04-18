// Package app wires runtime configuration into running bot instances.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/channel/telegram"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/execution"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/provider/openaicodex"
	"github.com/q15co/q15/systems/agent/internal/provider/openaicompatible"
	q15skills "github.com/q15co/q15/systems/agent/internal/skills"
	"github.com/q15co/q15/systems/agent/internal/tools"
)

type runtimeEnvironmentInfo struct {
	WorkspaceDir        string
	MemoryDir           string
	MediaDir            string
	SkillsDir           string
	ExecutorType        string
	ProxyEnabled        bool
	ProxyPolicyRevision string
}

func runBot(ctx context.Context, rt config.AgentRuntime) error {
	token := strings.TrimSpace(rt.TelegramToken)
	if token == "" {
		return errors.New("telegram token is required")
	}

	interactiveModelRefs := normalizeModelList(rt.InteractiveModels)
	if len(interactiveModelRefs) == 0 {
		return errors.New("at least one model is required")
	}
	cognitionModelRefs := normalizeModelList(rt.CognitionModels)
	if len(cognitionModelRefs) == 0 {
		cognitionModelRefs = append([]string(nil), interactiveModelRefs...)
	}

	executionClient, executionInfo, err := connectExecutionService(ctx, &rt.Execution)
	if err != nil {
		return fmt.Errorf("connect execution service for agent %q: %w", rt.Name, err)
	}
	defer executionClient.Close()

	runtimeInfo, err := resolveRuntimeEnvironment(executionInfo)
	if err != nil {
		return fmt.Errorf("resolve runtime environment for agent %q: %w", rt.Name, err)
	}
	mediaStore, err := q15media.NewFileStore(runtimeInfo.MediaDir)
	if err != nil {
		return fmt.Errorf("initialize media store for agent %q: %w", rt.Name, err)
	}
	modelAdapter, err := newModelAdapter(
		mergeModelRuntimes(rt.InteractiveModels, rt.CognitionModels),
		mediaStore,
	)
	if err != nil {
		return err
	}

	skillManager := q15skills.NewManager(q15skills.Settings{
		WorkspaceLocalDir:   rt.WorkspaceLocalDir,
		WorkspaceRuntimeDir: runtimeInfo.WorkspaceDir,
		SkillsLocalDir:      rt.SkillsLocalDir,
		SkillsRuntimeDir:    runtimeInfo.SkillsDir,
	})
	fileSettings := fileops.Settings{
		WorkspaceLocalDir:   rt.WorkspaceLocalDir,
		WorkspaceRuntimeDir: runtimeInfo.WorkspaceDir,
		MemoryLocalDir:      rt.MemoryLocalDir,
		MemoryRuntimeDir:    runtimeInfo.MemoryDir,
		SkillsLocalDir:      rt.SkillsLocalDir,
		SkillsRuntimeDir:    runtimeInfo.SkillsDir,
	}
	fileExec := q15skills.NewFileExecutor(fileops.NewExecutor(fileSettings), skillManager)
	braveAPIKey := strings.TrimSpace(os.Getenv("Q15_BRAVE_API_KEY"))
	toolList, err := buildToolList(
		executionClient,
		fileExec,
		skillManager,
		fileSettings,
		mediaStore,
		braveAPIKey,
	)
	if err != nil {
		return fmt.Errorf("configure tools for agent %q: %w", rt.Name, err)
	}

	toolRegistry, err := agent.NewToolRegistry(toolList...)
	if err != nil {
		return fmt.Errorf("build tool registry for agent %q: %w", rt.Name, err)
	}

	systemPrompt := agent.DefaultSystemPromptForName(rt.Name)
	systemPrompt = composeSystemPrompt(
		systemPrompt,
		rt.Name,
		runtimeInfo,
		toolRegistry.Definitions(),
	)

	memoryStore := memory.NewStore(rt.MemoryLocalDir, rt.Name, nil)
	if err := memoryStore.Init(ctx); err != nil {
		return fmt.Errorf("initialize memory store for agent %q: %w", rt.Name, err)
	}
	store := &runtimeStore{
		memory: memoryStore,
		skills: skillManager,
	}
	entryPoints := newRuntimeEntryPoints(runtimeEntryPointsConfig{
		modelClient:          modelAdapter,
		planner:              modelAdapter,
		tools:                toolRegistry,
		interactiveModelRefs: interactiveModelRefs,
		cognitionModelRefs:   cognitionModelRefs,
		interactivePrompt:    systemPrompt,
		interactiveStore:     store,
		controllerStore:      store,
		loader:               store,
		recentTurns:          rt.MemoryRecentTurns,
	})
	botAgent := entryPoints.NewInteractiveAgent()
	cognitionController, err := entryPoints.NewCognitionController(cognitionJobs()...)
	if err != nil {
		return fmt.Errorf("configure cognition controller for agent %q: %w", rt.Name, err)
	}
	if cognitionController != nil {
		store.AddAppendObserver(cognitionController.NotifyStateChange)
	}
	messageBus := bus.New(bus.DefaultBufferSize)

	channel, err := telegram.NewChannel(token, func(msg telegram.IncomingMessage) {
		err := messageBus.PublishInbound(ctx, telegramInboundMessage(msg))
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "publish inbound error: %v\n", err)
		}
	},
		telegram.WithMediaStore(mediaStore),
		telegram.WithAllowedUserIDs(rt.TelegramAllowedUserIDs),
	)
	if err != nil {
		return err
	}
	if err := channel.Start(ctx); err != nil {
		return err
	}

	telegramEndpoint := telegram.NewAgentEndpoint(channel)
	errCh := make(chan error, 2)
	go func() {
		errCh <- runAgentWorker(ctx, messageBus, botAgent, telegramEndpoint)
	}()
	if cognitionController != nil {
		go func() {
			errCh <- cognitionController.Run(ctx)
		}()
	}

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

func cognitionJobs() []cognition.JobRegistration {
	return []cognition.JobRegistration{
		cognition.NewVerificationReviewRegistration(),
		cognition.NewWorkingMemoryConsolidationRegistration(),
	}
}

func telegramInboundMessage(msg telegram.IncomingMessage) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:   bus.ChannelTelegram,
		ChatID:    msg.ChatID,
		UserID:    msg.UserID,
		MessageID: msg.MessageID,
		SentAt:    msg.SentAt,
		Text:      msg.Text,
		Media:     append([]string(nil), msg.Media...),
	}
}

func buildToolList(
	execClient execution.Service,
	fileExec tools.FileToolExecutor,
	skillManager *q15skills.Manager,
	fileSettings fileops.Settings,
	mediaStore q15media.Store,
	braveAPIKey string,
) ([]agent.Tool, error) {
	toolList := []agent.Tool{
		tools.NewReadFile(fileExec),
		tools.NewWriteFile(fileExec),
		tools.NewEditFile(fileExec),
		tools.NewApplyPatch(fileExec),
		tools.NewValidateSkill(skillManager),
		tools.NewExec(execClient),
		tools.NewLoadImage(fileSettings, mediaStore),
		tools.NewWebFetch(),
	}

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
	info runtimeEnvironmentInfo,
	toolDefs []agent.ToolDefinition,
) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = agent.DefaultSystemPromptForName(agentName)
	}

	parts := []string{base}
	if runtimeSection := renderRuntimeEnvironmentPrompt(agentName, info); runtimeSection != "" {
		parts = append(parts, runtimeSection)
	}
	if toolAdviceSection := renderToolAdvicePrompt(toolDefs); toolAdviceSection != "" {
		parts = append(parts, toolAdviceSection)
	}
	return strings.Join(parts, "\n\n")
}

func renderRuntimeEnvironmentPrompt(
	agentName string,
	info runtimeEnvironmentInfo,
) string {
	var lines []string
	nowLocal := time.Now().In(time.Local)
	agentName = strings.TrimSpace(agentName)
	if agentName != "" {
		lines = append(
			lines,
			fmt.Sprintf("- Agent name (authoritative from config): %s", agentName),
			"- If memory files mention a different agent name, treat that as stale and update those files.",
		)
	}
	lines = append(
		lines,
		fmt.Sprintf(
			"- Runtime local timezone for user-facing dates and times: %s.",
			describeRuntimeLocalTimezone(nowLocal),
		),
		"- Unless the user explicitly asks for another timezone, interpret and present dates and times in this runtime local timezone rather than UTC.",
		"- Prompt-visible <message_meta .../> tags use this same runtime local timezone for their local weekday and timestamp fields.",
	)
	if info.WorkspaceDir != "" {
		lines = append(
			lines,
			fmt.Sprintf(
				"- Workspace: %s",
				info.WorkspaceDir,
			),
		)
	}
	if info.SkillsDir != "" {
		lines = append(
			lines,
			fmt.Sprintf(
				"- Shared skills root: %s",
				info.SkillsDir,
			),
		)
	}
	if info.MemoryDir != "" {
		lines = append(
			lines,
			fmt.Sprintf(
				"- Persistent memory repo: %s",
				info.MemoryDir,
			),
			fmt.Sprintf(
				"- Core self-model files (auto-injected into prompt each turn): %s/core/*.md (seeded with AGENT.md, USER.md, SOUL.md)",
				info.MemoryDir,
			),
			fmt.Sprintf(
				"- Canonical working-memory file (auto-injected into prompt each turn): %s/working/WORKING_MEMORY.md",
				info.MemoryDir,
			),
			fmt.Sprintf(
				"- Additional persistent memory layers (tool-fetched, not auto-injected): %s/semantic, %s/history, %s/cognition",
				info.MemoryDir,
				info.MemoryDir,
				info.MemoryDir,
			),
			fmt.Sprintf(
				"- Other files under %s/working are not implicitly prompt-visible; only WORKING_MEMORY.md is auto-injected.",
				info.MemoryDir,
			),
			fmt.Sprintf(
				"- Transcript sequence bookkeeping lives under %s/history/state/head.json.",
				info.MemoryDir,
			),
			fmt.Sprintf(
				"- Auxiliary notebook files live under %s/notes/inbox, %s/notes/zettel, and %s/notes/maps using the built-in zettelkasten layout; they are never implicit prompt-visible working state.",
				info.MemoryDir,
				info.MemoryDir,
				info.MemoryDir,
			),
		)
	}
	if info.MediaDir != "" {
		lines = append(
			lines,
			fmt.Sprintf("- Runtime media root: %s", info.MediaDir),
			"- Image inputs are stored in the runtime media root and passed to providers via media refs; do not treat it as a normal text-edit root.",
		)
	}
	if info.ExecutorType != "" {
		lines = append(
			lines,
			fmt.Sprintf("- Command runtime: q15-exec sessions via %s", info.ExecutorType),
		)
	}
	if info.ProxyEnabled {
		revision := strings.TrimSpace(info.ProxyPolicyRevision)
		if revision == "" {
			revision = "present"
		}
		lines = append(
			lines,
			fmt.Sprintf(
				"- Proxy-mediated exec env injection is enabled (policy revision: %s).",
				revision,
			),
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
		"```text\n*** Begin Patch\n*** Update File: /memory/notes/inbox/todo.md\n@@\n unchanged line\n-old value\n+new value\n unchanged tail\n*** End Patch\n```",
		"- Prefer exec for commands, builds, tests, formatting, git, and other CLI workflows, not for routine file reads or edits.",
		"- Every exec call must include a non-empty `packages` array of nix installables (for example `nixpkgs#git`).",
		"- Use exec by providing the user command in `command` and the needed nix installables in `packages`; the execution service starts a session, streams stdout/stderr internally, and returns when the command exits.",
		"- Use exec for proxy-authenticated CLI flows such as `gh`, `git`, or `curl` when q15 is deployed with a separate q15-proxy instance.",
		"- First run may bootstrap nix and fetch package indexes, so network access is required.",
		"- Browser-specific command presets are not built in; use exec directly with explicit browser packages when needed.",
	)
	lines = append(
		lines,
		"- Use web_fetch for known web page URLs: it returns cleaned markdown plus slice metadata and is preferred over using exec with curl for ordinary webpage reads.",
		"- Use web_search for discovering current sources, then use web_fetch on a chosen result URL when you need page contents.",
	)
	if len(lines) == 0 {
		return ""
	}

	return agent.RenderPromptElement("runtime_environment", nil, strings.Join(lines, "\n"))
}

func describeRuntimeLocalTimezone(now time.Time) string {
	locationName := strings.TrimSpace(now.Location().String())
	zoneName, offsetSeconds := now.Zone()
	offsetText := formatUTCOffset(offsetSeconds)
	switch {
	case locationName != "" &&
		!strings.EqualFold(locationName, "Local") &&
		zoneName != "" &&
		zoneName != locationName:
		return fmt.Sprintf("%s (%s, %s)", locationName, zoneName, offsetText)
	case locationName != "" && !strings.EqualFold(locationName, "Local"):
		return fmt.Sprintf("%s (%s)", locationName, offsetText)
	case zoneName != "":
		return fmt.Sprintf("%s (%s)", zoneName, offsetText)
	default:
		return offsetText
	}
}

func formatUTCOffset(offsetSeconds int) string {
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	hours := offsetSeconds / 3600
	minutes := (offsetSeconds % 3600) / 60
	return fmt.Sprintf("UTC%s%02d:%02d", sign, hours, minutes)
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

func resolveRuntimeEnvironment(
	info *execpb.GetRuntimeInfoResponse,
) (runtimeEnvironmentInfo, error) {
	if info == nil {
		return runtimeEnvironmentInfo{}, errors.New("exec service returned empty runtime info")
	}
	workspaceDir := strings.TrimSpace(info.GetWorkspaceDir())
	if workspaceDir == "" {
		return runtimeEnvironmentInfo{}, errors.New(
			"exec service runtime info is missing workspace_dir",
		)
	}
	memoryDir := strings.TrimSpace(info.GetMemoryDir())
	if memoryDir == "" {
		return runtimeEnvironmentInfo{}, errors.New(
			"exec service runtime info is missing memory_dir",
		)
	}
	mediaDir := strings.TrimSpace(info.GetMediaDir())
	if mediaDir == "" {
		return runtimeEnvironmentInfo{}, errors.New(
			"exec service runtime info is missing media_dir",
		)
	}
	skillsDir := strings.TrimSpace(info.GetSkillsDir())
	if skillsDir == "" {
		return runtimeEnvironmentInfo{}, errors.New(
			"exec service runtime info is missing skills_dir",
		)
	}
	executorType := strings.TrimSpace(info.GetExecutorType())
	if executorType == "" {
		return runtimeEnvironmentInfo{}, errors.New(
			"exec service runtime info is missing executor_type",
		)
	}
	return runtimeEnvironmentInfo{
		WorkspaceDir:        workspaceDir,
		MemoryDir:           memoryDir,
		MediaDir:            mediaDir,
		SkillsDir:           skillsDir,
		ExecutorType:        executorType,
		ProxyEnabled:        info.GetProxyEnabled(),
		ProxyPolicyRevision: strings.TrimSpace(info.GetProxyPolicyRevision()),
	}, nil
}

type routedModelAdapter struct {
	endpoints map[string]routedModelEndpoint
}

type routedModelEndpoint struct {
	client        agent.ModelClient
	providerModel string
	capabilities  config.ModelCapabilities
}

type modelClientFactory func(config.AgentModelRuntime, q15media.Store) (agent.ModelClient, error)

var _ agent.ModelClient = (*routedModelAdapter)(nil)

func (r *routedModelAdapter) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
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

func newModelAdapter(
	models []config.AgentModelRuntime,
	mediaStore q15media.Store,
) (*routedModelAdapter, error) {
	return newModelAdapterWithFactory(models, mediaStore, defaultModelClientFactory)
}

func newModelAdapterWithFactory(
	models []config.AgentModelRuntime,
	mediaStore q15media.Store,
	clientFactory modelClientFactory,
) (*routedModelAdapter, error) {
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
			client, err = clientFactory(modelCfg, mediaStore)
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

func defaultModelClientFactory(
	modelCfg config.AgentModelRuntime,
	mediaStore q15media.Store,
) (agent.ModelClient, error) {
	switch strings.ToLower(strings.TrimSpace(modelCfg.ProviderType)) {
	case "openai-compatible":
		return openaicompatible.NewClient(
			modelCfg.ProviderBaseURL,
			modelCfg.ProviderAPIKey,
			mediaStore,
		)
	case "openai-codex":
		return openaicodex.NewClient(mediaStore)
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

func mergeModelRuntimes(
	interactiveModels []config.AgentModelRuntime,
	cognitionModels []config.AgentModelRuntime,
) []config.AgentModelRuntime {
	if len(interactiveModels) == 0 && len(cognitionModels) == 0 {
		return nil
	}

	out := make([]config.AgentModelRuntime, 0, len(interactiveModels)+len(cognitionModels))
	seen := make(map[string]struct{}, len(interactiveModels)+len(cognitionModels))

	for _, modelSet := range [][]config.AgentModelRuntime{interactiveModels, cognitionModels} {
		for _, model := range modelSet {
			ref := strings.TrimSpace(model.Ref)
			if ref == "" {
				continue
			}
			if _, ok := seen[ref]; ok {
				continue
			}
			seen[ref] = struct{}{}
			out = append(out, model)
		}
	}

	return out
}

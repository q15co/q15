// Package app wires runtime configuration into running bot instances.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/channel/telegram"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/selectionstore"
	q15skills "github.com/q15co/q15/systems/agent/internal/skills"
)

// runtimeEnvironmentInfo is the resolved view of the q15-exec runtime that the
// prompt and wiring consume.
type runtimeEnvironmentInfo struct {
	WorkspaceDir        string
	MemoryDir           string
	MediaDir            string
	SkillsDir           string
	ExecutorType        string
	ProxyEnabled        bool
	ProxyPolicyRevision string
}

// runBot is the composition root: it builds the selection store, model adapter,
// tools, system prompt, memory store, cognition controller, and the Telegram
// channel, then runs the interactive agent and cognition loop until the context
// is canceled or a worker fails.
func runBot(ctx context.Context, rt config.AgentRuntime, registry *modelcatalog.Registry) error {
	token := strings.TrimSpace(rt.TelegramToken)
	if token == "" {
		return errors.New("telegram token is required")
	}

	jobs := cognitionJobs()
	selectionStore, err := selectionstore.Open(selectionstore.DefaultPath(rt.WorkspaceLocalDir))
	if err != nil {
		return fmt.Errorf("open model selection store: %w", err)
	}
	selection, err := loadInteractiveSelection(registry, selectionStore)
	if err != nil {
		return err
	}
	interactiveModelRefSource := func() []string {
		return buildModelRefs(selection.CurrentModel(), registry)
	}
	cognitionRefResolver := newCognitionRefResolver(registry, selection, selectionStore)
	cognitionJobTypes := cognitionJobTypeNames(jobs)

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
	modelAdapter, err := newModelAdapter(registry, selection, mediaStore)
	if err != nil {
		return err
	}

	skillManager, fileSettings := buildSkillManager(rt, runtimeInfo)
	fileExec := q15skills.NewFileExecutor(fileops.NewExecutor(fileSettings), skillManager)
	embeddingService, err := newEmbeddingService(ctx, rt, fileSettings)
	if err != nil {
		return fmt.Errorf("configure embeddings for agent %q: %w", rt.Name, err)
	}
	if embeddingService != nil {
		defer embeddingService.Close()
	}
	baseToolList, err := buildToolList(
		executionClient,
		fileExec,
		skillManager,
		fileSettings,
		mediaStore,
		embeddingService,
		rt.Tools.WebSearch.BraveAPIKey,
	)
	if err != nil {
		return fmt.Errorf("configure tools for agent %q: %w", rt.Name, err)
	}
	baseToolRegistry, err := agent.NewToolRegistry(baseToolList...)
	if err != nil {
		return fmt.Errorf("build base tool registry for agent %q: %w", rt.Name, err)
	}
	toolRegistry, err := agent.NewToolRegistry(buildRuntimeTools(
		baseToolList,
		registry,
		selection,
		selectionStore,
		cognitionJobTypes,
		baseToolRegistry,
		mediaStore,
		skillManager,
	)...)
	if err != nil {
		return fmt.Errorf("build tool registry for agent %q: %w", rt.Name, err)
	}

	systemPrompt := composeSystemPrompt(
		agent.DefaultSystemPromptForName(rt.Name),
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
		modelClient:               modelAdapter,
		planner:                   modelAdapter,
		tools:                     toolRegistry,
		interactiveModelRefSource: interactiveModelRefSource,
		cognitionRefResolver:      cognitionRefResolver,
		interactivePrompt:         systemPrompt,
		interactiveSystemTextHints: []agent.SystemTextSource{
			func() string { return renderCurrentModelPrompt(registry, selection, selectionStore) },
		},
		interactiveStore: store,
		controllerStore:  store,
		loader:           store,
		recentTurns:      rt.MemoryRecentTurns,
	})
	botAgent := entryPoints.NewInteractiveAgent()
	cognitionController, err := entryPoints.NewCognitionController(jobs...)
	if err != nil {
		return fmt.Errorf("configure cognition controller for agent %q: %w", rt.Name, err)
	}
	if cognitionController != nil {
		store.AddAppendObserver(cognitionController.NotifyStateChange)
	}
	return runTelegramLoop(ctx, token, mediaStore, rt, botAgent, cognitionController)
}

// buildSkillManager constructs the skill manager and the shared file-operation
// settings used by both the file executor and the embedding service.
func buildSkillManager(
	rt config.AgentRuntime,
	info runtimeEnvironmentInfo,
) (*q15skills.Manager, fileops.Settings) {
	skillManager := q15skills.NewManager(q15skills.Settings{
		WorkspaceLocalDir:   rt.WorkspaceLocalDir,
		WorkspaceRuntimeDir: info.WorkspaceDir,
		SkillsLocalDir:      rt.SkillsLocalDir,
		SkillsRuntimeDir:    info.SkillsDir,
	})
	settings := fileops.Settings{
		WorkspaceLocalDir:   rt.WorkspaceLocalDir,
		WorkspaceRuntimeDir: info.WorkspaceDir,
		MemoryLocalDir:      rt.MemoryLocalDir,
		MemoryRuntimeDir:    info.MemoryDir,
		SkillsLocalDir:      rt.SkillsLocalDir,
		SkillsRuntimeDir:    info.SkillsDir,
	}
	return skillManager, settings
}

// runTelegramLoop starts the channel and runs the interactive agent and
// cognition controller workers, returning when the context is canceled or a
// worker fails.
func runTelegramLoop(
	ctx context.Context,
	token string,
	mediaStore q15media.Store,
	rt config.AgentRuntime,
	botAgent agent.Agent,
	cognitionController *cognition.Controller,
) error {
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

// cognitionJobs registers the built-in background cognition jobs.
func cognitionJobs() []cognition.JobRegistration {
	return []cognition.JobRegistration{
		cognition.NewVerificationReviewRegistration(),
		cognition.NewSemanticMemoryExtractionRegistration(),
		cognition.NewWorkingMemoryConsolidationRegistration(),
	}
}

// telegramInboundMessage adapts a Telegram channel message to the bus shape.
func telegramInboundMessage(msg telegram.IncomingMessage) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:     bus.ChannelTelegram,
		ChatID:      msg.ChatID,
		UserID:      msg.UserID,
		MessageID:   msg.MessageID,
		SentAt:      msg.SentAt,
		Text:        msg.Text,
		Attachments: conversation.CloneParts(msg.Attachments),
	}
}

// resolveRuntimeEnvironment converts the q15-exec runtime info response into the
// runtimeEnvironmentInfo the prompt and wiring consume, validating that all
// required roots are present.
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

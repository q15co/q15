// Package app wires runtime configuration into running bot instances.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/channel/bridge"
	"github.com/q15co/q15/systems/agent/internal/channel/telegram"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/dump"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/schedule"
	"github.com/q15co/q15/systems/agent/internal/schedulestore"
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
// tools, system prompt, memory store, cognition controller and the chat bridge
// listener, then hands the assembled runtime to runRuntime, which starts the
// channels and runs the workers until the context is canceled or one fails.
func runBot(ctx context.Context, rt config.AgentRuntime, registry *modelcatalog.Registry) error {
	if err := clearRuntimeReady(runtimeReadyPath); err != nil {
		return fmt.Errorf("clear stale runtime readiness: %w", err)
	}
	defer func() {
		if err := clearRuntimeReady(runtimeReadyPath); err != nil {
			log.Printf("q15: runtime event=readiness_cleanup_failed error=%q", err)
		}
	}()

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
	// Payload dump: set Q15_DUMP_PAYLOADS to a file path (or "stderr") to
	// capture canonical and raw wire request/response JSONL for debugging or
	// live demos.
	dumpWriter, dumpCloser := openDumpWriter()
	if dumpCloser != nil {
		defer dumpCloser()
	}

	var dumpRT http.RoundTripper
	if dumpWriter != nil {
		dumpRT = dump.NewTransportDump(http.DefaultTransport, dumpWriter)
	}

	factory := makeDumpAwareFactory(dumpRT)
	modelAdapter, err := newModelAdapterWithSelectionAndFactory(
		registry,
		selection,
		mediaStore,
		factory,
	)
	if err != nil {
		return err
	}
	var modelClient agent.ModelClient = modelAdapter
	if dumpWriter != nil {
		modelClient = dump.NewModelClientDump(modelAdapter, dumpWriter)
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

	memoryRepository := memoryrepo.New(rt.MemoryLocalDir, nil)
	memoryStore := memory.NewStore(memoryRepository, rt.Name)
	if err := memoryStore.Init(ctx); err != nil {
		return fmt.Errorf("initialize memory store for agent %q: %w", rt.Name, err)
	}
	bridgeServer, err := newBridgeServer(rt, memoryStore)
	if err != nil {
		return err
	}
	if bridgeServer != nil {
		// runBot owns the bound listener from here on. Any startup step below
		// can fail and return before the worker loop ever runs, and without
		// this the socket and its file descriptor would stay bound until the
		// process exits. It also runs on the normal shutdown path, where the
		// listener is already closed; closing it again is safe.
		defer bridgeServer.Close()
	}
	scheduleStore := schedulestore.New(filepath.Join(rt.StateLocalDir, "schedule"))
	if err := scheduleStore.Init(ctx); err != nil {
		return fmt.Errorf("initialize schedule store for agent %q: %w", rt.Name, err)
	}
	store := &runtimeStore{
		memory: memoryStore,
		skills: skillManager,
	}
	messageBus := bus.New(bus.DefaultBufferSize)
	scheduledExecutor, err := newScheduledJobExecutor(
		modelAdapter,
		baseToolRegistry,
		func(toolDefinitions []agent.ToolDefinition) *agent.ContextBuilder {
			return agent.NewContextBuilder(
				composeSystemPrompt(
					agent.DefaultSystemPromptForName(rt.Name),
					rt.Name,
					runtimeInfo,
					toolDefinitions,
				),
				store,
				rt.MemoryRecentTurns,
			)
		},
	)
	if err != nil {
		return fmt.Errorf("configure scheduled job executor for agent %q: %w", rt.Name, err)
	}
	scheduledToolNames := make(map[string]struct{}, len(baseToolRegistry.Definitions()))
	for _, definition := range baseToolRegistry.Definitions() {
		scheduledToolNames[definition.Name] = struct{}{}
	}
	scheduleManager, err := schedule.NewManager(ctx, schedule.Config{
		Store:     scheduleStore,
		Executor:  scheduledExecutor,
		Publisher: messageBus,
		DeliveryRecorder: &scheduledDeliveryRecorder{
			store: store,
		},
		MaxJobs:        rt.Tools.Schedule.MaxJobs,
		MaxTurns:       rt.Tools.Schedule.MaxRunTurns,
		AllowedUserIDs: rt.TelegramAllowedUserIDs,
		DefaultModel: func() schedule.ModelTarget {
			provider, ref := selection.Current()
			return schedule.ModelTarget{Provider: provider, Ref: ref}
		},
		ModelExists: func(target schedule.ModelTarget) bool {
			_, ok := registry.Lookup(target.Provider, target.Ref)
			return ok
		},
		ToolExists: func(name string) bool {
			_, ok := scheduledToolNames[name]
			return ok
		},
	})
	if err != nil {
		return fmt.Errorf("configure schedule manager for agent %q: %w", rt.Name, err)
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
		dumpWriter,
		scheduleManager,
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

	entryPoints := newRuntimeEntryPoints(runtimeEntryPointsConfig{
		modelClient:               modelClient,
		planner:                   modelAdapter,
		tools:                     toolRegistry,
		interactiveModelRefSource: interactiveModelRefSource,
		cognitionRefResolver:      cognitionRefResolver,
		interactivePrompt:         systemPrompt,
		interactiveSystemTextHints: buildInteractiveSystemTextHints(
			registry,
			selection,
			selectionStore,
		),
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
	return runRuntime(ctx, runtimeInputs{
		messageBus: messageBus,
		telegram: telegramSettings{
			token:          token,
			mediaStore:     mediaStore,
			allowedUserIDs: rt.TelegramAllowedUserIDs,
		},
		bridge:          bridgeServer,
		botAgent:        botAgent,
		scheduleManager: scheduleManager,
		cognition:       cognitionController,
	})
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

// telegramSettings is everything the Telegram transport is built from, kept as
// one value so that a transport's details travel together instead of sitting
// loose beside the runtime's services.
type telegramSettings struct {
	token          string
	mediaStore     q15media.Store
	allowedUserIDs []int64
}

// newChannel builds the transport, binding its inbound publisher to ctx so a
// canceled runtime stops publishing instead of queueing work for an agent that
// is shutting down. An allow-list is always supplied: the adapter's
// WithAllowedUserIDs option rejects an empty one rather than reading it as
// "everyone".
func (s telegramSettings) newChannel(
	ctx context.Context,
	messageBus *bus.Bus,
) (*telegram.Channel, error) {
	return telegram.NewChannel(s.token, func(msg telegram.IncomingMessage) {
		err := messageBus.PublishInbound(ctx, telegramInboundMessage(msg))
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "publish inbound error: %v\n", err)
		}
	},
		telegram.WithMediaStore(s.mediaStore),
		telegram.WithAllowedUserIDs(s.allowedUserIDs),
	)
}

// runtimeInputs is the runtime's parts: where its transports meet, what runs,
// and what the transports are built from.
//
// It is a struct rather than a parameter list because that list grows with the
// runtime: a field named at the call site cannot be passed in the wrong order,
// and a field added for one transport does not renumber another's arguments.
type runtimeInputs struct {
	// messageBus carries inbound messages from a transport to the agent
	// worker, and outbound messages back out to the channel that produced
	// them.
	messageBus *bus.Bus

	// The transports, which are not alternatives to each other and neither of
	// which names the runtime.
	//
	// telegram is settings rather than a built channel because the adapter
	// binds its inbound publisher to the runtime's context, which exists only
	// inside runRuntime. bridge is built by runBot instead, because binding its
	// listener can fail on startup and runBot owns closing it on the failure
	// paths before the worker loop runs.
	telegram telegramSettings
	bridge   *bridge.Server

	// The workers. The agent and the scheduler are required; cognition and the
	// bridge listener join them when they are configured.
	botAgent        agent.Agent
	scheduleManager *schedule.Manager
	cognition       *cognition.Controller
}

// runRuntime starts the runtime's channels, marks it ready, and then runs its
// workers until the context is canceled or a worker fails.
//
// The transports are not alternatives to each other. The Telegram adapter
// long-polls and publishes what it receives onto the runtime's bus, which is
// how anything reaches the agent; the chat bridge is a server, serving the
// frozen chat contract on its own listener and answering its clients directly,
// with nothing Telegram-shaped about it. This function owns both, so it is
// named for the runtime it runs rather than for either transport, and the
// bridge listener is one of the workers below rather than a sibling of this
// call. With no listen target configured the bridge is nil, and the runtime
// starts the Telegram channel alone.
func runRuntime(ctx context.Context, in runtimeInputs) error {
	if in.messageBus == nil {
		return errors.New("message bus is required")
	}
	if in.botAgent == nil {
		return errors.New("bot agent is required")
	}
	if in.scheduleManager == nil {
		return errors.New("schedule manager is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	channel, err := in.telegram.newChannel(runCtx, in.messageBus)
	if err != nil {
		return err
	}
	if err := channel.Start(runCtx); err != nil {
		return err
	}
	if err := markRuntimeReady(runtimeReadyPath, time.Now()); err != nil {
		return fmt.Errorf("mark agent runtime ready: %w", err)
	}
	log.Printf("q15: runtime event=ready")

	// The worker set: one loops over inbound messages and runs the agent, one
	// delivers outbound messages, one runs scheduled jobs. Cognition and the
	// bridge listener join them when they are configured. runRuntimeWorkers
	// returns on the first failure or on cancellation, then joins the rest.
	telegramEndpoint := telegram.NewAgentEndpoint(channel)
	workers := []runtimeWorker{
		func(workerCtx context.Context) error {
			return runAgentWorker(workerCtx, in.messageBus, in.botAgent, telegramEndpoint)
		},
		func(workerCtx context.Context) error {
			return runOutboundWorker(workerCtx, in.messageBus, telegramEndpoint)
		},
		func(workerCtx context.Context) error {
			return in.scheduleManager.Run(workerCtx)
		},
	}
	if in.cognition != nil {
		workers = append(workers, in.cognition.Run)
	}
	if in.bridge != nil {
		workers = append(workers, in.bridge.Serve)
	}

	return runRuntimeWorkers(runCtx, cancel, runtimeWorkerShutdownTimeout, workers...)
}

// newBridgeServer binds the chat-contract bridge listener when the runtime
// configures a target. An empty target disables the listener: the shared
// socket volume that backs it is provisioned by the later compose slice, so a
// deployment can run without the bridge until that volume exists. A configured
// target that fails to bind is fatal and names the path, never silent.
func newBridgeServer(
	rt config.AgentRuntime,
	lister bridge.TurnLister,
) (*bridge.Server, error) {
	target := strings.TrimSpace(rt.BridgeListenTarget)
	if target == "" {
		log.Printf("q15: runtime event=bridge_disabled reason=listen_target_unset")
		return nil, nil
	}
	return bridge.NewServer(target, bridge.NewService(lister))
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

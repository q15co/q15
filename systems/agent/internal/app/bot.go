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
	channelport "github.com/q15co/q15/systems/agent/internal/channel"
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
// tools, system prompt, memory store, cognition controller and the transports,
// then hands the assembled runtime to runRuntime, which marks it ready and runs
// its workers until the context is canceled or one fails.
func runBot(ctx context.Context, rt config.AgentRuntime, registry *modelcatalog.Registry) error {
	if err := clearRuntimeReady(runtimeReadyPath); err != nil {
		return fmt.Errorf("clear stale runtime readiness: %w", err)
	}
	defer func() {
		if err := clearRuntimeReady(runtimeReadyPath); err != nil {
			log.Printf("q15: runtime event=readiness_cleanup_failed error=%q", err)
		}
	}()

	// Empty when the agent configures no Telegram at all. The transport is
	// optional: the runtime is built from the transports a deployment
	// configures, and runRuntime is what refuses a runtime that ends up with
	// none. At this layer the bridge supplies no channel endpoint yet, so a
	// Telegram-less runtime is refused there rather than run headless.
	token := strings.TrimSpace(rt.TelegramToken)

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
	messageBus := bus.New(bus.DefaultBufferSize)
	bridgeServer, bridgeEndpoint, err := bridgeSettings{
		listenTarget: rt.BridgeListenTarget,
	}.newServer(memoryStore, messageBus)
	if err != nil {
		return err
	}
	var transportWorkers []runtimeWorker
	if bridgeServer != nil {
		// runBot owns the bound listener from here on. Any startup step below
		// can fail and return before the worker loop ever runs, and without
		// this the socket and its file descriptor would stay bound until the
		// process exits. It also runs on the normal shutdown path, where the
		// listener is already closed; closing it again is safe.
		defer bridgeServer.Close()
		transportWorkers = append(transportWorkers, bridgeServer.Serve)
	}
	scheduleStore := schedulestore.New(filepath.Join(rt.StateLocalDir, "schedule"))
	if err := scheduleStore.Init(ctx); err != nil {
		return fmt.Errorf("initialize schedule store for agent %q: %w", rt.Name, err)
	}
	store := &runtimeStore{
		memory: memoryStore,
		skills: skillManager,
	}
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
		MaxJobs:  rt.Tools.Schedule.MaxJobs,
		MaxTurns: rt.Tools.Schedule.MaxRunTurns,
		// The owner allow-list is the Telegram one because Telegram is the
		// only transport here that carries user identities to check. With
		// Telegram unconfigured it is empty, which the manager reads as no
		// restriction; that is not an escalation, because whoever reaches the
		// bridge can already run the agent and every tool it holds. A
		// per-channel owner policy is the change to make if a second transport
		// ever brings identities of its own.
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
	// The transports are assembled here, at the composition root, because that
	// is where a transport's startup can fail and where the listener it bound
	// has to be closed on the later failure paths. Inbound publishing is bound
	// to runCtx, which is the same context runRuntime cancels when its workers
	// are shutting down, so a canceled runtime stops publishing instead of
	// queueing work for an agent that is going away. Both transports publish
	// onto the runtime's bus, which is how anything reaches the agent.
	//
	// Both transports are optional, and the endpoint guard in runRuntime is what
	// refuses a runtime that ends up with no channel endpoint to be reached
	// through.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	telegramTransport := telegramSettings{
		token:          token,
		mediaStore:     mediaStore,
		allowedUserIDs: rt.TelegramAllowedUserIDs,
	}
	var agentEndpoints []channelport.AgentEndpoint
	var outboundEndpoints []channelport.OutboundEndpoint
	if telegramTransport.configured() {
		telegramChannel, err := telegramTransport.newChannel(runCtx, messageBus)
		if err != nil {
			return err
		}
		if err := telegramChannel.Start(runCtx); err != nil {
			return err
		}
		telegramEndpoint := telegram.NewAgentEndpoint(telegramChannel)
		agentEndpoints = append(agentEndpoints, telegramEndpoint)
		outboundEndpoints = append(outboundEndpoints, telegramEndpoint)
	}
	// The bridge endpoint joins the worker's registries only when its listener
	// is running: without a listener there is nothing to answer its rpcs.
	if bridgeEndpoint != nil {
		agentEndpoints = append(agentEndpoints, bridgeEndpoint)
	}

	return runRuntime(runCtx, cancel, runtimeInputs{
		messageBus:        messageBus,
		agentEndpoints:    agentEndpoints,
		outboundEndpoints: outboundEndpoints,
		transportWorkers:  transportWorkers,
		botAgent:          botAgent,
		scheduleManager:   scheduleManager,
		cognition:         cognitionController,
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

// configured reports whether the deployment configures Telegram at all. The
// transport is optional: a deployment that names no token names no Telegram,
// and the runtime then builds no channel and long-polls for nothing.
func (s telegramSettings) configured() bool {
	return strings.TrimSpace(s.token) != ""
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

	// The transports, as the endpoints the workers route to. Neither is named
	// here and neither is an alternative to the other: the runtime does not
	// have a transport, it has however many the deployment configured, so they
	// arrive as lists.
	agentEndpoints    []channelport.AgentEndpoint
	outboundEndpoints []channelport.OutboundEndpoint

	// transportWorkers are the workers a transport needs run. A polling
	// transport has none of its own; the chat bridge's listener is one.
	transportWorkers []runtimeWorker

	// The workers. The agent and the scheduler are required; cognition joins
	// them when it is configured.
	botAgent        agent.Agent
	scheduleManager *schedule.Manager
	cognition       *cognition.Controller
}

// runRuntime marks the runtime ready and then runs its workers until the
// context is canceled or a worker fails.
//
// It knows nothing about which transports the runtime has. They arrive as the
// endpoints its workers route to, plus the workers those transports need run,
// and the composition root assembled both. Adding a transport is therefore a
// change to runBot and to nothing here.
//
// ctx and cancel must be the pair the transports bound their inbound publishing
// to: cancel is what stops them as the workers shut down.
func runRuntime(ctx context.Context, cancel context.CancelFunc, in runtimeInputs) error {
	if in.messageBus == nil {
		return errors.New("message bus is required")
	}
	if in.botAgent == nil {
		return errors.New("bot agent is required")
	}
	if in.scheduleManager == nil {
		return errors.New("schedule manager is required")
	}
	// Refused before the ready marker rather than inside the workers, so a
	// runtime with nothing to be reached through fails as part of startup
	// rather than in a goroutine after readiness. buildEndpointRegistry refuses
	// the same thing again, which is what covers a list that carries only nils.
	if len(in.agentEndpoints) == 0 {
		return errors.New("at least one channel endpoint is required")
	}
	if err := markRuntimeReady(runtimeReadyPath, time.Now()); err != nil {
		return fmt.Errorf("mark agent runtime ready: %w", err)
	}
	log.Printf("q15: runtime event=ready")

	// The worker set: one loops over inbound messages and runs the agent, one
	// delivers outbound messages, one runs scheduled jobs. Cognition and any
	// transport that needs a worker of its own join them when they are
	// configured. runRuntimeWorkers returns on the first failure or on
	// cancellation, then joins the rest.
	workers := []runtimeWorker{
		func(workerCtx context.Context) error {
			return runAgentWorker(workerCtx, in.messageBus, in.botAgent, in.agentEndpoints...)
		},
		func(workerCtx context.Context) error {
			return runOutboundWorker(workerCtx, in.messageBus, in.outboundEndpoints...)
		},
		func(workerCtx context.Context) error {
			return in.scheduleManager.Run(workerCtx)
		},
	}
	if in.cognition != nil {
		workers = append(workers, in.cognition.Run)
	}
	workers = append(workers, in.transportWorkers...)

	return runRuntimeWorkers(ctx, cancel, runtimeWorkerShutdownTimeout, workers...)
}

// bridgeSettings is the chat bridge listener's own configuration, kept as one
// value so that a transport's details travel together instead of sitting loose
// beside the runtime's services, the way telegramSettings keeps Telegram's. The
// transcript lister and the publisher the listener also needs are built from the
// store and the bus rather than from config, so they stay newServer parameters.
type bridgeSettings struct {
	listenTarget string
}

// newServer binds the chat-contract bridge listener and builds its agent
// endpoint when the runtime configures a target. An empty target disables the
// listener: the shared socket volume that backs it is provisioned by the later
// compose slice, so a deployment can run without the bridge until that volume
// exists. A configured target that fails to bind is fatal and names the path,
// never silent.
func (s bridgeSettings) newServer(
	lister bridge.TurnLister,
	publisher bridge.InboundPublisher,
) (*bridge.Server, *bridge.AgentEndpoint, error) {
	target := strings.TrimSpace(s.listenTarget)
	if target == "" {
		log.Printf("q15: runtime event=bridge_disabled reason=listen_target_unset")
		return nil, nil, nil
	}
	endpoint := bridge.NewAgentEndpoint(publisher)
	server, err := bridge.NewServer(target, bridge.NewService(lister, endpoint))
	if err != nil {
		return nil, nil, err
	}
	return server, endpoint, nil
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

package app

import (
	"context"
	"fmt"
	"io"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/execution"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/schedule"
	"github.com/q15co/q15/systems/agent/internal/selectionstore"
	q15skills "github.com/q15co/q15/systems/agent/internal/skills"
	"github.com/q15co/q15/systems/agent/internal/tools"
	"github.com/q15co/q15/systems/agent/internal/tools/subagent"
)

// buildRuntimeTools appends model selection, delegated sub-agent, and
// owner-scoped scheduling tools to the base tool list. The base registry is
// reused as the restricted sub-agent and scheduled-job tool surface.
func buildRuntimeTools(
	baseToolList []agent.Tool,
	registry *modelcatalog.Registry,
	selection *modelcatalog.Selection,
	selectionStore *selectionstore.Store,
	baseToolRegistry agent.ToolRegistry,
	mediaStore q15media.Store,
	skillManager *q15skills.Manager,
	dumpWriter io.Writer,
	scheduleManager *schedule.Manager,
	modelAdapter *routedModelAdapter,
	contextFitter agent.RequestContextFitter,
) []agent.Tool {
	subAgentManager := subagent.NewManager(
		registry,
		func(model modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			return modelAdapter.clientFor(model)
		},
		baseToolRegistry,
		mediaStore,
		subagentSkillResolver{manager: skillManager},
		dumpWriter,
		contextFitter,
	)
	return append(
		baseToolList,
		tools.NewListProviders(registry, selection),
		tools.NewListModels(registry, selection),
		tools.NewDecisionCheck(newRoutedDecisionAdapter(registry, modelAdapter.decisionClientFor)),
		tools.NewSwitchModel(registry, selection, selectionStore),
		subagent.NewSpawn(subAgentManager),
		subagent.NewRead(subAgentManager),
		subagent.NewWrite(subAgentManager),
		subagent.NewList(subAgentManager),
		subagent.NewKill(subAgentManager),
		tools.NewScheduleCreate(scheduleManager),
		tools.NewScheduleList(scheduleManager),
		tools.NewScheduleRuns(scheduleManager),
		tools.NewScheduleUpdate(scheduleManager),
		tools.NewScheduleDelete(scheduleManager),
	)
}

// toolDeps carries the dependencies buildToolList assembles the base tool
// list from: constructed services plus the resolved web_search configuration.
type toolDeps struct {
	execClient       execution.Service
	fileExec         tools.FileToolExecutor
	skillManager     *q15skills.Manager
	fileSettings     fileops.Settings
	mediaStore       q15media.Store
	embeddingService *embed.Service
	webSearch        config.WebSearchToolRuntime
}

// buildToolList assembles the base agent tools (file ops, exec, media, skills,
// web, and optional embeddings/web_search) that every agent exposes before the
// runtime-only model-selection and sub-agent tools are appended. web_search is
// registered only when at least one provider is configured.
func buildToolList(deps toolDeps) ([]agent.Tool, error) {
	toolList := []agent.Tool{
		tools.NewReadFile(deps.fileExec),
		tools.NewWriteFile(deps.fileExec),
		tools.NewEditFile(deps.fileExec),
		tools.NewApplyPatch(deps.fileExec),
		tools.NewValidateSkill(deps.skillManager),
		tools.NewExec(deps.execClient),
		tools.NewExecList(deps.execClient),
		tools.NewExecRead(deps.execClient),
		tools.NewExecWrite(deps.execClient),
		tools.NewExecKill(deps.execClient),
		tools.NewLoadImage(deps.fileSettings, deps.mediaStore),
		tools.NewAttachMedia(deps.fileSettings, deps.mediaStore),
		tools.NewWebFetch(),
	}

	if deps.embeddingService != nil {
		embedJobs := embed.NewSyncJobManager(deps.embeddingService)
		toolList = append(
			toolList,
			tools.NewEmbedSources(deps.embeddingService),
			tools.NewEmbedSync(embedJobs),
			tools.NewEmbedSearch(deps.embeddingService),
			tools.NewEmbedStatus(deps.embeddingService),
			tools.NewEmbedJob(embedJobs),
		)
	}

	providers, err := tools.NewSearchProviders(tools.WebSearchConfig{
		BraveAPIKey:  deps.webSearch.BraveAPIKey,
		OllamaAPIKey: deps.webSearch.OllamaAPIKey,
	})
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return toolList, nil
	}
	webSearchTool, err := tools.NewWebSearch(providers)
	if err != nil {
		return nil, err
	}
	return append(toolList, webSearchTool), nil
}

// newEmbeddingService constructs the typed embedding service when configured.
// The embedding backend is selected by the configured provider via
// embed.NewEmbedder (gemini by default). It returns nil (no service, no tools)
// when embeddings are disabled.
func newEmbeddingService(
	ctx context.Context,
	rt config.AgentRuntime,
	fileSettings fileops.Settings,
) (*embed.Service, error) {
	tool := rt.Tools.Embeddings
	if !tool.Enabled {
		return nil, nil
	}
	settings := embed.Settings{
		WorkspaceLocalDir: fileSettings.WorkspaceLocalDir,
		MemoryLocalDir:    fileSettings.MemoryLocalDir,
		SkillsLocalDir:    fileSettings.SkillsLocalDir,
		QdrantURL:         tool.QdrantURL,
		Provider:          tool.Provider,
		GeminiAPIKey:      tool.GeminiAPIKey,
		APIKey:            tool.APIKey,
		BaseURL:           tool.BaseURL,
		Model:             tool.Model,
		Dimensions:        tool.Dimensions,
		BatchSize:         tool.BatchSize,
	}
	state, err := embed.OpenState(ctx, settings)
	if err != nil {
		return nil, err
	}
	vectors, err := embed.NewQdrantStore(tool.QdrantURL, tool.QdrantAPIKey, tool.Dimensions)
	if err != nil {
		_ = state.Close()
		return nil, err
	}
	embedder, err := embed.NewEmbedder(ctx, settings)
	if err != nil {
		_ = state.Close()
		_ = vectors.Close()
		return nil, err
	}
	return embed.NewService(settings, state, vectors, embedder), nil
}

// subagentSkillResolver adapts the skills.Manager to the subagent package's
// SkillResolver interface. It lives in app, where both packages are visible,
// to avoid an import cycle between internal/skills and internal/tools/subagent.
type subagentSkillResolver struct {
	manager *q15skills.Manager
}

// ResolveSkill implements subagent.SkillResolver by delegating to the skills
// manager and mapping the result into the subagent package's neutral Skill type.
func (r subagentSkillResolver) ResolveSkill(ref string) (subagent.Skill, error) {
	if r.manager == nil {
		return subagent.Skill{}, fmt.Errorf("skills manager is not configured")
	}
	resolved, err := r.manager.ResolveSkill(ref)
	if err != nil {
		return subagent.Skill{}, err
	}
	return subagent.Skill{
		Name:          resolved.Name,
		Description:   resolved.Description,
		Source:        string(resolved.Source),
		SkillPath:     resolved.SkillPath,
		SkillFilePath: resolved.SkillFilePath,
		Body:          resolved.Body,
		Tools:         append([]string(nil), resolved.Tools...),
	}, nil
}

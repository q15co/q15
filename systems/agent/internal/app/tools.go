package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/execution"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/selectionstore"
	q15skills "github.com/q15co/q15/systems/agent/internal/skills"
	"github.com/q15co/q15/systems/agent/internal/tools"
	"github.com/q15co/q15/systems/agent/internal/tools/subagent"
)

// buildRuntimeTools appends the runtime-only tools (model selection and
// delegated sub-agent sessions) to the base tool list. The base registry is
// reused as the sub-agent's tool surface.
func buildRuntimeTools(
	baseToolList []agent.Tool,
	registry *modelcatalog.Registry,
	selection *modelcatalog.Selection,
	selectionStore *selectionstore.Store,
	cognitionJobTypes []string,
	baseToolRegistry agent.ToolRegistry,
	mediaStore q15media.Store,
	skillManager *q15skills.Manager,
) []agent.Tool {
	subAgentManager := subagent.NewManager(
		registry,
		defaultModelClientFactory,
		baseToolRegistry,
		mediaStore,
		subagentSkillResolver{manager: skillManager},
	)
	return append(
		baseToolList,
		tools.NewListProviders(registry, selection),
		tools.NewListModels(registry, selection),
		tools.NewSwitchModel(registry, selection, selectionStore),
		tools.NewSwitchCognitionModel(registry, selectionStore, cognitionJobTypes),
		subagent.NewSpawn(subAgentManager),
		subagent.NewRead(subAgentManager),
		subagent.NewWrite(subAgentManager),
		subagent.NewList(subAgentManager),
		subagent.NewKill(subAgentManager),
	)
}

// buildToolList assembles the base agent tools (file ops, exec, media, skills,
// web, and optional embeddings/web_search) that every agent exposes before the
// runtime-only model-selection and sub-agent tools are appended.
func buildToolList(
	execClient execution.Service,
	fileExec tools.FileToolExecutor,
	skillManager *q15skills.Manager,
	fileSettings fileops.Settings,
	mediaStore q15media.Store,
	embeddingService *embed.Service,
	braveAPIKey string,
) ([]agent.Tool, error) {
	toolList := []agent.Tool{
		tools.NewReadFile(fileExec),
		tools.NewWriteFile(fileExec),
		tools.NewEditFile(fileExec),
		tools.NewApplyPatch(fileExec),
		tools.NewValidateSkill(skillManager),
		tools.NewExec(execClient),
		tools.NewExecList(execClient),
		tools.NewExecRead(execClient),
		tools.NewExecWrite(execClient),
		tools.NewExecKill(execClient),
		tools.NewLoadImage(fileSettings, mediaStore),
		tools.NewAttachAudio(fileSettings, mediaStore),
		tools.NewAttachImage(fileSettings, mediaStore),
		tools.NewWebFetch(),
	}

	if embeddingService != nil {
		toolList = append(
			toolList,
			tools.NewEmbedSources(embeddingService),
			tools.NewEmbedSync(embeddingService),
			tools.NewEmbedSearch(embeddingService),
			tools.NewEmbedStatus(embeddingService),
		)
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

// newEmbeddingService constructs the typed embedding service when configured.
// It returns nil (no service, no tools) when embeddings are disabled.
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
		GeminiAPIKey:      tool.GeminiAPIKey,
		Model:             tool.Model,
		Dimensions:        tool.Dimensions,
	}
	state, err := embed.OpenState(ctx, settings)
	if err != nil {
		return nil, err
	}
	vectors, err := embed.NewQdrantStore(tool.QdrantURL, tool.Dimensions)
	if err != nil {
		_ = state.Close()
		return nil, err
	}
	embedder, err := embed.NewGeminiEmbedder(ctx, tool.GeminiAPIKey, tool.Model, tool.Dimensions)
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

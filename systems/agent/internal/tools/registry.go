// Package tools provides a thin re-export layer that collects all agent tools
// from their individual sub-packages.
//
// Each tool lives in its own sub-package (exec, files, skills, web) so that
// tool-specific helpers can coexist without polluting a shared namespace.
package tools

import (
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/execution"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"

	embedtools "github.com/q15co/q15/systems/agent/internal/tools/embed"
	"github.com/q15co/q15/systems/agent/internal/tools/exec"
	"github.com/q15co/q15/systems/agent/internal/tools/files"
	mediatools "github.com/q15co/q15/systems/agent/internal/tools/media"
	"github.com/q15co/q15/systems/agent/internal/tools/skills"
	"github.com/q15co/q15/systems/agent/internal/tools/subagent"
	"github.com/q15co/q15/systems/agent/internal/tools/web"
)

// FileToolExecutor performs rooted file operations inside the runtime-visible roots.
type FileToolExecutor = files.FileToolExecutor

// NewReadFile delegates to files.NewReadFile.
func NewReadFile(exec FileToolExecutor) *files.ReadFile {
	return files.NewReadFile(exec)
}

// NewWriteFile delegates to files.NewWriteFile.
func NewWriteFile(exec FileToolExecutor) *files.WriteFile {
	return files.NewWriteFile(exec)
}

// NewEditFile delegates to files.NewEditFile.
func NewEditFile(exec FileToolExecutor) *files.EditFile {
	return files.NewEditFile(exec)
}

// NewApplyPatch delegates to files.NewApplyPatch.
func NewApplyPatch(exec FileToolExecutor) *files.ApplyPatch {
	return files.NewApplyPatch(exec)
}

// NewExec delegates to exec.NewExec.
func NewExec(client execution.Service) *exec.Exec {
	return exec.NewExec(client)
}

// NewExecList delegates to exec.NewList.
func NewExecList(client execution.Service) *exec.List {
	return exec.NewList(client)
}

// NewExecRead delegates to exec.NewRead.
func NewExecRead(client execution.Service) *exec.Read {
	return exec.NewRead(client)
}

// NewExecWrite delegates to exec.NewWrite.
func NewExecWrite(client execution.Service) *exec.Write {
	return exec.NewWrite(client)
}

// NewExecKill delegates to exec.NewKill.
func NewExecKill(client execution.Service) *exec.Kill {
	return exec.NewKill(client)
}

// NewLoadImage delegates to media.NewLoadImage.
func NewLoadImage(paths fileops.Settings, mediaStore q15media.Store) *mediatools.LoadImage {
	return mediatools.NewLoadImage(paths, mediaStore)
}

// NewAttachAudio delegates to media.NewAttachAudio.
func NewAttachAudio(paths fileops.Settings, mediaStore q15media.Store) *mediatools.AttachAudio {
	return mediatools.NewAttachAudio(paths, mediaStore)
}

// NewValidateSkill delegates to skills.NewValidateSkill.
func NewValidateSkill(validator skills.SkillValidator) *skills.ValidateSkill {
	return skills.NewValidateSkill(validator)
}

// NewWebFetch delegates to web.NewFetch.
func NewWebFetch() *web.Fetch {
	return web.NewFetch()
}

// NewBraveWebSearch delegates to web.NewBraveWebSearch.
func NewBraveWebSearch(apiKey string) (*web.BraveWebSearch, error) {
	return web.NewBraveWebSearch(apiKey)
}

// NewEmbedSources delegates to embedtools.NewSources.
func NewEmbedSources(service *embed.Service) *embedtools.Sources {
	return embedtools.NewSources(service)
}

// NewEmbedSync delegates to embedtools.NewSync.
func NewEmbedSync(service *embed.Service) *embedtools.Sync {
	return embedtools.NewSync(service)
}

// NewEmbedSearch delegates to embedtools.NewSearch.
func NewEmbedSearch(service *embed.Service) *embedtools.Search {
	return embedtools.NewSearch(service)
}

// NewEmbedStatus delegates to embedtools.NewStatus.
func NewEmbedStatus(service *embed.Service) *embedtools.Status {
	return embedtools.NewStatus(service)
}

// NewSubAgentManager constructs a delegated sub-agent session manager.
func NewSubAgentManager(
	models []config.AgentModelRuntime,
	factory subagent.ModelFactory,
	baseTools agent.ToolRegistry,
	mediaStore q15media.Store,
) *subagent.Manager {
	return subagent.NewManager(models, factory, baseTools, mediaStore)
}

package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/selectionstore"
)

// composeSystemPrompt assembles the static base system prompt from the default
// prompt for the agent name, the runtime environment section, and the tool
// advice section. Per-turn dynamic sections (such as the current model) are
// injected separately by the loop.
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

// renderCurrentModelPrompt renders the per-turn current-model section: the live
// interactive provider/model with discovery metadata, any cognition overrides,
// and the change-model guidance. Discovered values are quoted so a malformed
// provider/model id cannot inject prompt content.
func renderCurrentModelPrompt(
	registry *modelcatalog.Registry,
	selection *modelcatalog.Selection,
	store *selectionstore.Store,
) string {
	if registry == nil || selection == nil {
		return ""
	}

	provider, ref := selection.Current()
	provider = strings.TrimSpace(provider)
	ref = strings.TrimSpace(ref)
	if provider == "" && ref == "" {
		return ""
	}

	var modelLine string
	if m, ok := registry.Lookup(provider, ref); ok {
		modelLine = fmt.Sprintf(
			"model: %s (%s)",
			promptQuote(m.Ref),
			promptQuote(modelPromptMetadata(m)),
		)
	} else {
		modelLine = fmt.Sprintf(
			"model: %s (%s)",
			promptQuote(ref),
			promptQuote("not in current live roster — provider down or model deprecated"),
		)
	}
	body := strings.Join([]string{
		"## Current model",
		fmt.Sprintf("provider: %s", promptQuote(provider)),
		modelLine,
		renderCognitionOverrides(store),
		"",
		"## Changing models",
		"You are not locked to this model. Use `list_providers` and `list_models` to see alternatives with live metadata, then `switch_model` to change the interactive model, or `switch_cognition_model` to set the model for a specific background cognition job. Switch only when you have a specific reason (capability needed, larger context, fresher model, cost). Otherwise stay.",
	}, "\n")
	body = strings.TrimSpace(body)
	return agent.RenderPromptElement("current_model", nil, body)
}

// renderCognitionOverrides renders per-job cognition model overrides. Jobs
// without an explicit override inherit the interactive model and are omitted.
func renderCognitionOverrides(store *selectionstore.Store) string {
	if store == nil {
		return ""
	}
	overrides := store.CognitionSelections()
	if len(overrides) == 0 {
		return ""
	}
	jobTypes := make([]string, 0, len(overrides))
	for jobType := range overrides {
		jobTypes = append(jobTypes, jobType)
	}
	sort.Strings(jobTypes)
	lines := []string{"## Cognition model overrides"}
	for _, jobType := range jobTypes {
		override := overrides[jobType]
		lines = append(lines, fmt.Sprintf(
			"- %s: provider %s, model %s",
			promptQuote(jobType),
			promptQuote(override.Provider),
			promptQuote(override.Model),
		))
	}
	lines = append(lines, "- Jobs without an override inherit the current interactive model.")
	return strings.Join(lines, "\n")
}

// promptQuote quotes a model-facing value as JSON so discovered fields with
// newlines or XML-like delimiters cannot break out of the prompt element.
func promptQuote(value string) string {
	data, err := json.Marshal(strings.TrimSpace(value))
	if err != nil {
		return "\"\""
	}
	return string(data)
}

// modelPromptMetadata renders one model's discovery metadata as a compact
// capability/size/context/release/cost summary line.
func modelPromptMetadata(m modelcatalog.Model) string {
	parts := make([]string, 0, 8)
	capabilities := modelPromptCapabilities(m)
	if len(capabilities) > 0 {
		parts = append(parts, strings.Join(capabilities, ", "))
	}
	if m.ParameterCount > 0 {
		parts = append(parts, fmt.Sprintf("%s params", formatParameterCount(m.ParameterCount)))
	}
	if m.MaxContextTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d context", m.MaxContextTokens))
	}
	if m.MaxOutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d output", m.MaxOutputTokens))
	}
	if !m.ReleaseDate.IsZero() {
		parts = append(parts, fmt.Sprintf("released %s", m.ReleaseDate.Format(time.DateOnly)))
	}
	if m.CostTier != "" {
		parts = append(parts, "cost "+m.CostTier)
	}
	if len(parts) == 0 {
		return "metadata unknown"
	}
	return strings.Join(parts, " | ")
}

// modelPromptCapabilities returns the human-readable capability labels present
// on one model.
func modelPromptCapabilities(m modelcatalog.Model) []string {
	capabilities := make([]string, 0, 7)
	if m.Capabilities.ToolCalling {
		capabilities = append(capabilities, "tools")
	}
	if m.Capabilities.Reasoning {
		capabilities = append(capabilities, "reasoning")
	}
	if m.Capabilities.ImageInput {
		capabilities = append(capabilities, "vision")
	}
	if m.Capabilities.AudioInput {
		capabilities = append(capabilities, "audio")
	}
	if m.VideoInput {
		capabilities = append(capabilities, "video")
	}
	if m.StructuredOutput {
		capabilities = append(capabilities, "structured output")
	}
	return capabilities
}

// formatParameterCount renders a parameter count with a B/M suffix.
func formatParameterCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.0fB", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.0fM", float64(n)/1_000_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// renderRuntimeEnvironmentPrompt renders the runtime environment section:
// workspace/memory/media roots, command runtime, proxy state, and the file/exec
// tool guidance that does not vary per registered tool.
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
				"- Canonical semantic-memory files are %s/semantic/facts.md, %s/semantic/preferences.md, and %s/semantic/projects.md.",
				info.MemoryDir,
				info.MemoryDir,
				info.MemoryDir,
			),
			"- When editing canonical semantic memory, preserve these exact H2 headings:",
			"- facts.md: Confirmed Facts, Grounded Inferences",
			"- preferences.md: User Preferences, Collaboration Preferences",
			"- projects.md: Active Projects, Durable Project Knowledge",
			"- Do not invent new top-level sections in canonical semantic memory; merge content into the existing headings instead.",
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
		"- The exec `packages` array is optional; omit it or pass `[]` for commands that only need the runtime shell, and include nix installables only when the command needs extra tools (for example `[\"nixpkgs#git\"]`).",
		"- Use exec by providing the user command in `command` and optional nix installables in `packages`; the execution service starts a session, streams stdout/stderr internally, and returns when the command exits.",
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

// describeRuntimeLocalTimezone renders the runtime local timezone for the
// prompt, preferring a named location, then a zone abbreviation, then an offset.
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

// formatUTCOffset renders a UTC offset (in seconds) as UTC±HH:MM.
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

// renderToolAdvicePrompt renders the registered tools' PromptGuidance into the
// tool_advice prompt section.
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

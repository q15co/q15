package agent

import (
	"fmt"
	"strings"
)

func injectCoreMemory(systemText string, core CoreMemory) string {
	systemText = strings.TrimSpace(systemText)
	if len(core.Files) == 0 {
		return systemText
	}

	files := make([]string, 0, len(core.Files))
	for _, file := range core.Files {
		path := strings.TrimSpace(file.RelativePath)
		if path == "" {
			continue
		}
		attrs := map[string]string{
			"path": path,
		}
		if desc := strings.TrimSpace(file.Description); desc != "" {
			attrs["description"] = desc
		}
		if file.Limit > 0 {
			attrs["limit"] = fmt.Sprintf("%d", file.Limit)
		}
		rendered := RenderPromptElement("core_file", attrs, file.Content)
		if rendered == "" {
			continue
		}
		files = append(files, rendered)
	}
	if len(files) == 0 {
		return systemText
	}

	return strings.TrimSpace(systemText + "\n\n" + RenderPromptElement(
		"core_memory",
		nil,
		strings.Join(files, "\n\n"),
	))
}

func injectSkillCatalog(systemText string, catalog SkillCatalog) string {
	systemText = strings.TrimSpace(systemText)
	if len(catalog.Entries) == 0 && len(catalog.Warnings) == 0 {
		return systemText
	}

	parts := make([]string, 0, len(catalog.Entries)+len(catalog.Warnings))
	for _, entry := range catalog.Entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		attrs := map[string]string{
			"name": name,
		}
		if source := strings.TrimSpace(entry.Source); source != "" {
			attrs["source"] = source
		}
		if skillFilePath := strings.TrimSpace(entry.SkillFilePath); skillFilePath != "" {
			attrs["path"] = skillFilePath
		}
		body := strings.TrimSpace(entry.Description)
		if body == "" {
			body = "Available on demand."
		}
		rendered := RenderPromptElement("skill", attrs, body)
		if rendered == "" {
			continue
		}
		parts = append(parts, rendered)
	}
	for _, warning := range catalog.Warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		parts = append(parts, RenderPromptElement("warning", nil, warning))
	}
	if len(parts) == 0 {
		return systemText
	}

	return strings.TrimSpace(systemText + "\n\n" + RenderPromptElement(
		"skill_catalog",
		nil,
		strings.Join(parts, "\n\n"),
	))
}

func copyMessages(in []Message) []Message {
	if len(in) == 0 {
		return nil
	}

	out := make([]Message, len(in))
	for i, msg := range in {
		out[i] = msg
		if len(msg.ToolCalls) > 0 {
			out[i].ToolCalls = append([]ToolCall(nil), msg.ToolCalls...)
		}
		if len(msg.ProviderRaw) > 0 {
			out[i].ProviderRaw = append([]byte(nil), msg.ProviderRaw...)
		}
	}
	return out
}

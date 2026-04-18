package agent

import (
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func injectCoreMemory(core CoreMemory) (conversation.Message, bool) {
	if len(core.Files) == 0 {
		return conversation.Message{}, false
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
		return conversation.Message{}, false
	}

	return systemMessage(RenderPromptElement(
		"core_memory",
		nil,
		strings.Join(files, "\n\n"),
	)), true
}

func injectWorkingMemory(working WorkingMemory) (conversation.Message, bool) {
	path := strings.TrimSpace(working.RelativePath)
	content := strings.TrimSpace(working.Content)
	if path == "" || content == "" {
		return conversation.Message{}, false
	}

	return systemMessage(RenderPromptElement(
		"working_memory",
		map[string]string{"path": path},
		content,
	)), true
}

func injectSkillCatalog(catalog SkillCatalog) (conversation.Message, bool) {
	if len(catalog.Entries) == 0 && len(catalog.Warnings) == 0 {
		return conversation.Message{}, false
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
		return conversation.Message{}, false
	}

	return systemMessage(RenderPromptElement(
		"skill_catalog",
		nil,
		strings.Join(parts, "\n\n"),
	)), true
}

func copyMessages(in []conversation.Message) []conversation.Message {
	return conversation.CloneMessages(in)
}

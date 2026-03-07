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

	var out strings.Builder
	out.WriteString(systemText)
	out.WriteString("\n\n")
	out.WriteString("Core Memory (persistent; always in-context):\n")
	for _, file := range core.Files {
		path := strings.TrimSpace(file.RelativePath)
		if path == "" {
			continue
		}
		out.WriteString("<core_file path=\"")
		out.WriteString(path)
		out.WriteString("\"")
		if desc := strings.TrimSpace(file.Description); desc != "" {
			out.WriteString(" description=\"")
			out.WriteString(desc)
			out.WriteString("\"")
		}
		if file.Limit > 0 {
			out.WriteString(" limit=\"")
			out.WriteString(fmt.Sprintf("%d", file.Limit))
			out.WriteString("\"")
		}
		out.WriteString(">\n")
		if content := strings.TrimSpace(file.Content); content != "" {
			out.WriteString(content)
			out.WriteString("\n")
		}
		out.WriteString("</core_file>\n")
	}
	return out.String()
}

func injectSkillCatalog(systemText string, catalog SkillCatalog) string {
	systemText = strings.TrimSpace(systemText)
	if len(catalog.Entries) == 0 && len(catalog.Warnings) == 0 {
		return systemText
	}

	var out strings.Builder
	out.WriteString(systemText)
	out.WriteString("\n\n")
	out.WriteString("Available Skills (dynamic; load on demand):\n")
	for _, entry := range catalog.Entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		out.WriteString("- ")
		out.WriteString(name)
		if source := strings.TrimSpace(entry.Source); source != "" {
			out.WriteString(" [")
			out.WriteString(source)
			out.WriteString("]")
		}
		if desc := strings.TrimSpace(entry.Description); desc != "" {
			out.WriteString(": ")
			out.WriteString(desc)
		}
		if skillFilePath := strings.TrimSpace(entry.SkillFilePath); skillFilePath != "" {
			out.WriteString(" Load with read_file from ")
			out.WriteString(skillFilePath)
			out.WriteString(".")
		}
		out.WriteString("\n")
	}
	if len(catalog.Warnings) > 0 {
		out.WriteString("Skill Catalog Warnings:\n")
		for _, warning := range catalog.Warnings {
			warning = strings.TrimSpace(warning)
			if warning == "" {
				continue
			}
			out.WriteString("- ")
			out.WriteString(warning)
			out.WriteString("\n")
		}
	}
	return strings.TrimSpace(out.String())
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

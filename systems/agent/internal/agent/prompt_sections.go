package agent

import (
	"html"
	"sort"
	"strings"
)

// PromptSection represents one XML-like prompt block.
type PromptSection struct {
	Name       string
	Attributes map[string]string
	Body       string
}

// ComposePromptSections renders the provided prompt sections in order.
func ComposePromptSections(sections ...PromptSection) string {
	out := make([]string, 0, len(sections))
	for _, section := range sections {
		rendered := RenderPromptElement(section.Name, section.Attributes, section.Body)
		if rendered == "" {
			continue
		}
		out = append(out, rendered)
	}
	return strings.Join(out, "\n\n")
}

// RenderPromptElement renders one XML-like prompt element.
func RenderPromptElement(name string, attributes map[string]string, body string) string {
	name = strings.TrimSpace(name)
	body = strings.TrimSpace(body)
	if name == "" || body == "" {
		return ""
	}

	var out strings.Builder
	out.WriteByte('<')
	out.WriteString(name)

	if len(attributes) > 0 {
		keys := make([]string, 0, len(attributes))
		for key := range attributes {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := strings.TrimSpace(attributes[key])
			if value == "" {
				continue
			}
			out.WriteByte(' ')
			out.WriteString(key)
			out.WriteString(`="`)
			out.WriteString(html.EscapeString(value))
			out.WriteByte('"')
		}
	}

	out.WriteString(">\n")
	out.WriteString(body)
	out.WriteString("\n</")
	out.WriteString(name)
	out.WriteByte('>')
	return out.String()
}

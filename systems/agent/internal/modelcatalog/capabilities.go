package modelcatalog

import "maps"

// Has reports an effective normalized capability.
func (c Capabilities) Has(name string) bool {
	switch name {
	case "chat":
		return c.Chat
	case "embed":
		return c.Embed
	case "decision":
		return c.Decision
	case "text":
		return c.Text
	case "image_input":
		return c.ImageInput
	case "audio_input":
		return c.AudioInput
	case "tool_calling":
		return c.ToolCalling
	case "reasoning":
		return c.Reasoning
	}
	return false
}

var capabilityNames = []string{
	"chat",
	"embed",
	"decision",
	"text",
	"image_input",
	"audio_input",
	"tool_calling",
	"reasoning",
}

// Set applies evidence only when its source has at least the existing precedence.
// Unknown names are ignored by the temporary boolean representation pending #375.
func (c *Capabilities) Set(name string, supported bool, provenance string) {
	if provenanceRank(provenance) < provenanceRank(c.Provenance[name]) {
		return
	}
	switch name {
	case "chat":
		c.Chat = supported
	case "embed":
		c.Embed = supported
	case "decision":
		c.Decision = supported
	case "text":
		c.Text = supported
	case "image_input":
		c.ImageInput = supported
	case "audio_input":
		c.AudioInput = supported
	case "tool_calling":
		c.ToolCalling = supported
	case "reasoning":
		c.Reasoning = supported
	default:
		return
	}
	if provenance == "" {
		return
	}
	c.Provenance = maps.Clone(c.Provenance)
	if c.Provenance == nil {
		c.Provenance = make(map[string]string)
	}
	c.Provenance[name] = provenance
}

// WithProvenance labels positive evidence without treating omitted flags as denials.
func (c Capabilities) WithProvenance(source string) Capabilities {
	for _, name := range capabilityNames {
		if c.Has(name) && c.Provenance[name] == "" {
			c.Set(name, true, source)
		}
	}
	return c
}

func provenanceRank(source string) int {
	switch source {
	case "override":
		return 3
	case "derived":
		return 2
	case "declared":
		return 1
	}
	return 0
}

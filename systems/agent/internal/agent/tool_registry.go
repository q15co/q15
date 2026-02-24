package agent

import (
	"context"
	"fmt"
	"strings"
)

type Registry struct {
	definitions []ToolDefinition
	toolsByName map[string]Tool
}

var _ ToolRegistry = (*Registry)(nil)

func NewToolRegistry(tools ...Tool) (*Registry, error) {
	reg := &Registry{
		definitions: make([]ToolDefinition, 0, len(tools)),
		toolsByName: make(map[string]Tool, len(tools)),
	}

	for i, tool := range tools {
		if tool == nil {
			continue
		}

		def := tool.Definition()
		name := strings.TrimSpace(def.Name)
		if name == "" {
			return nil, fmt.Errorf("tool[%d] has empty name", i)
		}
		def.Name = name

		if _, exists := reg.toolsByName[name]; exists {
			return nil, fmt.Errorf("duplicate tool name %q", name)
		}

		reg.definitions = append(reg.definitions, def)
		reg.toolsByName[name] = tool
	}

	return reg, nil
}

func (r *Registry) Definitions() []ToolDefinition {
	if r == nil || len(r.definitions) == 0 {
		return nil
	}
	out := make([]ToolDefinition, len(r.definitions))
	copy(out, r.definitions)
	return out
}

func (r *Registry) Run(ctx context.Context, call ToolCall) (string, error) {
	if r == nil {
		return "", fmt.Errorf("no tool registry configured")
	}

	name := strings.TrimSpace(call.Name)
	if name == "" {
		return "", fmt.Errorf("tool call is missing name")
	}

	tool, ok := r.toolsByName[name]
	if !ok {
		return "", fmt.Errorf("unsupported tool: %s", name)
	}
	return tool.Run(ctx, call.Arguments)
}

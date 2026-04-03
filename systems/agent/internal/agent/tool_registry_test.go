package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type testTool struct {
	def ToolDefinition
	run func(context.Context, string) (string, error)
}

func (t *testTool) Definition() ToolDefinition {
	return t.def
}

func (t *testTool) Run(ctx context.Context, arguments string) (string, error) {
	if t.run == nil {
		return "", nil
	}
	return t.run(ctx, arguments)
}

type structuredTestTool struct {
	def       ToolDefinition
	runResult func(context.Context, string) (ToolResult, error)
}

func (t *structuredTestTool) Definition() ToolDefinition {
	return t.def
}

func (t *structuredTestTool) Run(context.Context, string) (string, error) {
	return "", nil
}

func (t *structuredTestTool) RunResult(ctx context.Context, arguments string) (ToolResult, error) {
	if t.runResult == nil {
		return ToolResult{}, nil
	}
	return t.runResult(ctx, arguments)
}

func TestNewToolRegistryAggregatesAndDispatches(t *testing.T) {
	var gotArgs string

	reg, err := NewToolRegistry(
		&testTool{
			def: ToolDefinition{Name: "one"},
			run: func(ctx context.Context, arguments string) (string, error) {
				_ = ctx
				gotArgs = arguments
				return "ok", nil
			},
		},
		&testTool{def: ToolDefinition{Name: "two"}},
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	defs := reg.Definitions()
	if len(defs) != 2 {
		t.Fatalf("Definitions len = %d, want 2", len(defs))
	}
	if defs[0].Name != "one" || defs[1].Name != "two" {
		t.Fatalf("Definitions names = [%q,%q], want [one,two]", defs[0].Name, defs[1].Name)
	}

	out, err := reg.Run(context.Background(), ToolCall{Name: "one", Arguments: `{"x":1}`})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out.Output != "ok" {
		t.Fatalf("Run().Output = %q, want %q", out.Output, "ok")
	}
	if gotArgs != `{"x":1}` {
		t.Fatalf("tool received arguments %q, want %q", gotArgs, `{"x":1}`)
	}
}

func TestToolRegistryUsesStructuredToolResultsWhenAvailable(t *testing.T) {
	reg, err := NewToolRegistry(&structuredTestTool{
		def: ToolDefinition{Name: "image"},
		runResult: func(context.Context, string) (ToolResult, error) {
			return ToolResult{
				Output:    "loaded",
				MediaRefs: []string{"media://sha256/abc"},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	got, err := reg.Run(context.Background(), ToolCall{Name: "image"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got.Output != "loaded" {
		t.Fatalf("Run().Output = %q, want %q", got.Output, "loaded")
	}
	if len(got.MediaRefs) != 1 || got.MediaRefs[0] != "media://sha256/abc" {
		t.Fatalf("Run().MediaRefs = %#v", got.MediaRefs)
	}
}

func TestNewToolRegistryRejectsDuplicateNames(t *testing.T) {
	_, err := NewToolRegistry(
		&testTool{def: ToolDefinition{Name: "dup"}},
		&testTool{def: ToolDefinition{Name: "dup"}},
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate tool name") {
		t.Fatalf("NewToolRegistry() error = %v, want duplicate name error", err)
	}
}

func TestNewToolRegistryRejectsEmptyName(t *testing.T) {
	_, err := NewToolRegistry(&testTool{def: ToolDefinition{Name: "  "}})
	if err == nil || !strings.Contains(err.Error(), "empty name") {
		t.Fatalf("NewToolRegistry() error = %v, want empty name error", err)
	}
}

func TestToolRegistryUnknownTool(t *testing.T) {
	reg, err := NewToolRegistry(&testTool{def: ToolDefinition{Name: "known"}})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	_, err = reg.Run(context.Background(), ToolCall{Name: "missing"})
	if err == nil || !strings.Contains(err.Error(), "unsupported tool") {
		t.Fatalf("Run() error = %v, want unsupported tool error", err)
	}
}

func TestToolRegistryDefinitionsReturnsCopy(t *testing.T) {
	reg, err := NewToolRegistry(&testTool{def: ToolDefinition{Name: "one", Description: "orig"}})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	defs := reg.Definitions()
	defs[0].Name = "changed"
	defs[0].Description = "changed"

	defs2 := reg.Definitions()
	if defs2[0].Name != "one" || defs2[0].Description != "orig" {
		t.Fatalf("Definitions() mutated registry contents: %+v", defs2[0])
	}
}

func TestToolRegistryAllowsNilTools(t *testing.T) {
	reg, err := NewToolRegistry(nil, &testTool{def: ToolDefinition{Name: "one"}})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	if len(reg.Definitions()) != 1 {
		t.Fatalf("Definitions len = %d, want 1", len(reg.Definitions()))
	}
}

func TestToolRegistryPropagatesToolError(t *testing.T) {
	reg, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "fail"},
		run: func(context.Context, string) (string, error) {
			return "", fmt.Errorf("boom")
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	_, err = reg.Run(context.Background(), ToolCall{Name: "fail"})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("Run() error = %v, want boom", err)
	}
}

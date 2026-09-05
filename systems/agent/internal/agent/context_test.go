package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type contextBuilderTestStore struct{}

func (contextBuilderTestStore) LoadRecentMessages(
	context.Context,
	int,
) ([]conversation.Message, error) {
	return []conversation.Message{conversation.AssistantMessage(
		conversation.Text("recent reply", conversation.TextDispositionFinal),
	)}, nil
}

func (contextBuilderTestStore) LoadCoreMemory(context.Context) (CoreMemory, error) {
	return CoreMemory{Files: []CoreMemoryFile{
		{RelativePath: "core/AGENT.md", Content: "core identity"},
	}}, nil
}

func (contextBuilderTestStore) LoadSkillCatalog(context.Context) (SkillCatalog, error) {
	return SkillCatalog{Entries: []SkillCatalogEntry{
		{Name: "research", Description: "research skill"},
	}}, nil
}

func (contextBuilderTestStore) LoadWorkingMemory(context.Context) (WorkingMemory, error) {
	return WorkingMemory{
		RelativePath: "working/WORKING_MEMORY.md",
		Content:      "active state",
	}, nil
}

func TestContextBuilderBuildsSharedPromptEnvelopeInStableOrder(t *testing.T) {
	builder := NewContextBuilder(
		"base prompt",
		contextBuilderTestStore{},
		3,
		func(context.Context) string { return "dynamic context" },
	)

	messages, err := builder.Build(context.Background(), conversation.UserMessage("task"))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(messages) != 6 {
		t.Fatalf("Build() messages = %#v, want 6 messages", messages)
	}

	wantText := []string{
		"base prompt",
		"core identity",
		"research skill",
		"active state",
		"recent reply",
		"task",
	}
	wantRoles := []conversation.Role{
		conversation.SystemRole,
		conversation.SystemRole,
		conversation.SystemRole,
		conversation.SystemRole,
		conversation.AssistantRole,
		conversation.UserRole,
	}
	for i := range messages {
		if messages[i].Role != wantRoles[i] {
			t.Fatalf("messages[%d].Role = %q, want %q", i, messages[i].Role, wantRoles[i])
		}
		text := conversation.TextValue(messages[i])
		if !strings.Contains(text, wantText[i]) {
			t.Fatalf("messages[%d] text = %q, want it to contain %q", i, text, wantText[i])
		}
	}
	if text := conversation.TextValue(messages[0]); !strings.Contains(text, "dynamic context") {
		t.Fatalf("base system text = %q, want dynamic context", text)
	}
}

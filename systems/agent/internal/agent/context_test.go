package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type contextBuilderTestStore struct {
	openLoops string
}

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

func (s contextBuilderTestStore) LoadOpenLoops(context.Context) (string, error) {
	return s.openLoops, nil
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

func TestContextBuilderInjectsOpenLoopsAfterWorkingMemory(t *testing.T) {
	builder := NewContextBuilder(
		"base prompt",
		contextBuilderTestStore{openLoops: "follow up on the deploy"},
		3,
	)

	messages, err := builder.Build(context.Background(), conversation.UserMessage("task"))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	wantText := []string{
		"base prompt",
		"core identity",
		"research skill",
		"active state",
		"follow up on the deploy",
		"recent reply",
		"task",
	}
	if len(messages) != len(wantText) {
		t.Fatalf("Build() messages = %#v, want %d messages", messages, len(wantText))
	}
	for i := range wantText {
		text := conversation.TextValue(messages[i])
		if !strings.Contains(text, wantText[i]) {
			t.Fatalf("messages[%d] text = %q, want it to contain %q", i, text, wantText[i])
		}
	}
	if messages[4].Role != conversation.SystemRole {
		t.Fatalf("open loops role = %q, want %q", messages[4].Role, conversation.SystemRole)
	}
}

func TestContextBuilderInjectsNonEmptyOpenLoopsOnce(t *testing.T) {
	const openLoops = "- reply to the reviewer\n- rerun the gate"
	builder := NewContextBuilder("base prompt", contextBuilderTestStore{openLoops: openLoops}, 3)

	messages, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	count := 0
	for _, message := range messages {
		text := conversation.TextValue(message)
		if strings.Contains(text, "<open_loops>") {
			count++
			if !strings.Contains(text, openLoops) {
				t.Fatalf("open loops message = %q, want it to contain %q", text, openLoops)
			}
		}
	}
	if count != 1 {
		t.Fatalf("open loops messages = %d, want 1", count)
	}
}

func TestContextBuilderSkipsEmptyOpenLoops(t *testing.T) {
	for name, openLoops := range map[string]string{
		"empty":      "",
		"whitespace": " \n\t\n ",
	} {
		t.Run(name, func(t *testing.T) {
			builder := NewContextBuilder(
				"base prompt",
				contextBuilderTestStore{openLoops: openLoops},
				3,
			)

			messages, err := builder.Build(context.Background())
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			// base, core, skills, working, recent: no open-loops message.
			if len(messages) != 5 {
				t.Fatalf("Build() messages = %#v, want 5 messages", messages)
			}
			for i, message := range messages {
				text := conversation.TextValue(message)
				if strings.Contains(text, "open_loops") {
					t.Fatalf("messages[%d] = %q, want no open loops", i, text)
				}
			}
		})
	}
}

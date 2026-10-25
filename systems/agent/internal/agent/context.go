package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// SystemTextSource returns dynamic text to append to the base system prompt on
// each model turn. The run context lets sources render transport-scoped or
// other request-scoped guidance without leaking it into unrelated entrypoints.
type SystemTextSource func(context.Context) string

// ContextStore supplies the persisted conversation replay used to build a
// prompt. Richer memory and skill sources are discovered through the focused
// optional store interfaces in this package.
type ContextStore interface {
	LoadRecentMessages(context.Context, int) ([]conversation.Message, error)
}

// RequestContextFitter rebuilds a provider request for its actual selected model.
// generatedStart separates the initial envelope from this run's generated messages.
// Trailing instructions must be counted and kept after any added request metadata.
type RequestContextFitter func(context.Context, ModelContext, []conversation.Message, []ToolDefinition, int, ...conversation.Message) ([]conversation.Message, error)

// PreparedContext keeps a run's immutable history snapshot and request fitting together.
type PreparedContext struct {
	Messages   []conversation.Message
	FitRequest RequestContextFitter
}

// ContextAssembler can retrieve history and fit it without changing the source transcript.
type ContextAssembler interface {
	PrepareContext(
		context.Context,
		[]conversation.Message,
		[]conversation.Message,
	) (PreparedContext, error)
}

// ContextBuilder assembles the shared prompt envelope used by interactive and
// unattended agent runs. It deliberately does not execute or persist a turn.
type ContextBuilder struct {
	store             ContextStore
	systemText        string
	systemTextSources []SystemTextSource
	recentTurns       int
}

// NewContextBuilder constructs a prompt context builder.
func NewContextBuilder(
	systemText string,
	store ContextStore,
	recentTurns int,
	systemTextSources ...SystemTextSource,
) *ContextBuilder {
	systemText = strings.TrimSpace(systemText)
	if systemText == "" {
		systemText = DefaultSystemPrompt
	}
	if recentTurns == 0 {
		recentTurns = defaultRecentTurns
	}
	return &ContextBuilder{
		store:             store,
		systemText:        systemText,
		systemTextSources: append([]SystemTextSource(nil), systemTextSources...),
		recentTurns:       recentTurns,
	}
}

// SystemText renders the stable base prompt plus the latest dynamic snippets.
func (b *ContextBuilder) SystemText(ctx context.Context) string {
	if b == nil {
		return DefaultSystemPrompt
	}
	if ctx == nil {
		ctx = context.Background()
	}
	parts := []string{b.systemText}
	for _, source := range b.systemTextSources {
		if source == nil {
			continue
		}
		text := strings.TrimSpace(source(ctx))
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Build returns the prepared prompt's canonical messages.
func (b *ContextBuilder) Build(
	ctx context.Context,
	input ...conversation.Message,
) ([]conversation.Message, error) {
	prepared, err := b.Prepare(ctx, input...)
	return prepared.Messages, err
}

// Prepare assembles the stable system prefix and a run-specific history snapshot.
func (b *ContextBuilder) Prepare(
	ctx context.Context,
	input ...conversation.Message,
) (PreparedContext, error) {
	systemMessages := []conversation.Message{systemMessage(b.SystemText(ctx))}
	if b != nil && b.store != nil {
		if coreStore, ok := b.store.(CoreMemoryStore); ok {
			coreMemory, err := coreStore.LoadCoreMemory(ctx)
			if err != nil {
				return PreparedContext{}, fmt.Errorf("load core memory: %w", err)
			}
			if message, ok := injectCoreMemory(coreMemory); ok {
				systemMessages = append(systemMessages, message)
			}
		}
		if skillStore, ok := b.store.(SkillCatalogStore); ok {
			skillCatalog, err := skillStore.LoadSkillCatalog(ctx)
			if err != nil {
				return PreparedContext{}, fmt.Errorf("load skill catalog: %w", err)
			}
			if message, ok := injectSkillCatalog(skillCatalog); ok {
				systemMessages = append(systemMessages, message)
			}
		}
		if assembler, ok := b.store.(ContextAssembler); ok {
			return assembler.PrepareContext(ctx, systemMessages, input)
		}
	}

	var recentMessages []conversation.Message
	if b != nil && b.store != nil {
		var err error
		recentMessages, err = b.store.LoadRecentMessages(ctx, b.recentTurns)
		if err != nil {
			return PreparedContext{}, fmt.Errorf("load recent messages: %w", err)
		}
	}

	messages := make(
		[]conversation.Message,
		0,
		len(systemMessages)+len(recentMessages)+len(input),
	)
	messages = append(messages, copyMessages(systemMessages)...)
	messages = append(messages, copyMessages(recentMessages)...)
	messages = append(messages, copyMessages(input)...)
	return PreparedContext{Messages: messages}, nil
}

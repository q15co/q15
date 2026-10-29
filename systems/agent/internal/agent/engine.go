package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
	"github.com/q15co/q15/systems/agent/internal/turnreply"
)

// ModelRefSource returns the ordered model refs to consider for one model
// turn.
type ModelRefSource func() []string

// StaticModelRefSource returns a source that always yields the provided refs.
func StaticModelRefSource(modelRefs []string) ModelRefSource {
	refs := normalizeModelRefs(modelRefs)
	return func() []string {
		return append([]string(nil), refs...)
	}
}

// Engine runs model/tool turns without assuming a user-facing chat turn or
// transcript persistence strategy.
type Engine struct {
	modelClient    ModelClient
	planner        modelselection.Planner
	tools          ToolRegistry
	modelRefSource ModelRefSource
	maxTurns       int
}

// EngineRequest describes one engine execution.
type EngineRequest struct {
	Messages           []conversation.Message
	UseTools           bool
	AllowedTools       []string
	ToolCallPolicy     ToolCallPolicy
	RequireToolCalling bool
	SystemTextSource   SystemTextSource
	Observer           RunObserver
	// MaxFailedModelAttempts bounds provider failures across all turns. Zero is unlimited.
	MaxFailedModelAttempts int
	ContextFitter          RequestContextFitter
	TurnBudget             *TurnBudget
}

// EngineResult captures the engine-produced messages and final outcome for one
// execution.
type EngineResult struct {
	Messages    []conversation.Message
	FinalText   string
	Attachments []conversation.Part
	MediaRefs   []string
	ModelRef    string
	Turn        int
}

// NewEngine constructs an Engine with default planner behavior.
func NewEngine(
	modelClient ModelClient,
	tools ToolRegistry,
	modelRefs []string,
) *Engine {
	return NewEngineWithPlanner(modelClient, nil, tools, modelRefs)
}

// NewEngineWithPlanner constructs an Engine with an explicit model-selection
// planner. When planner is nil, the configured model refs are used as-is.
func NewEngineWithPlanner(
	modelClient ModelClient,
	planner modelselection.Planner,
	tools ToolRegistry,
	modelRefs []string,
) *Engine {
	return NewEngineWithPlannerAndModelRefSource(
		modelClient,
		planner,
		tools,
		StaticModelRefSource(modelRefs),
	)
}

// NewEngineWithPlannerAndModelRefSource constructs an Engine whose model refs
// are read from source for each model turn. This lets runtime state changes,
// such as switch_model, take effect without rebuilding the engine.
func NewEngineWithPlannerAndModelRefSource(
	modelClient ModelClient,
	planner modelselection.Planner,
	tools ToolRegistry,
	source ModelRefSource,
) *Engine {
	if planner == nil {
		planner = modelselection.Passthrough{}
	}
	if source == nil {
		source = StaticModelRefSource(nil)
	}
	return &Engine{
		modelClient:    modelClient,
		planner:        planner,
		tools:          tools,
		modelRefSource: source,
		maxTurns:       defaultMaxTurns,
	}
}

// SetMaxTurns overrides the maximum model/tool turns for a run.
func (e *Engine) SetMaxTurns(maxTurns int) {
	if e == nil || maxTurns <= 0 {
		return
	}
	e.maxTurns = maxTurns
}

// Run executes one model/tool cycle over the provided initial messages.
func (e *Engine) Run(ctx context.Context, req EngineRequest) (EngineResult, error) {
	messages := conversation.NormalizeMessages(copyMessages(req.Messages))
	start := len(messages)

	var toolDefs []ToolDefinition
	var toolRegistry ToolRegistry
	if req.UseTools && e.tools != nil {
		toolRegistry = filterToolRegistry(e.tools, req.AllowedTools)
		toolDefs = toolRegistry.Definitions()
	}

	extractor := turnreply.NewExtractor(deliverToolNames(toolRegistry))

	loopDetector := newToolLoopDetector()
	emptyAssistantRetries := 0
	failureBudget := modelFailureBudget{limit: req.MaxFailedModelAttempts}
	var lastModelRef string
	var lastTurn int

	for turn := 0; turn < e.maxTurns; turn++ {
		if req.TurnBudget != nil && req.TurnBudget.Remaining() == 0 {
			break
		}
		requestMessages := copyMessages(messages)
		requestMessages = applySystemTextSource(ctx, requestMessages, req.SystemTextSource)
		requestStart := start + len(requestMessages) - len(messages)
		if emptyAssistantRetries > 0 {
			requestMessages = append(
				requestMessages,
				systemMessage(emptyResponseRetrySteeringPrompt),
			)
		}
		requestTools := toolDefs
		finalTurn := false
		var requestTail []conversation.Message
		if req.TurnBudget != nil {
			var note conversation.Message
			note, finalTurn = req.TurnBudget.next()
			requestTail = []conversation.Message{note}
			if finalTurn {
				requestTools = nil
			}
		}

		modelRef, result, err := e.completeWithObserver(
			ctx,
			requestMessages,
			requestTools,
			req.RequireToolCalling && len(requestTools) > 0,
			turn,
			req.Observer,
			&failureBudget,
			req.ContextFitter,
			requestStart,
			requestTail,
		)
		lastModelRef, lastTurn = modelRef, turn
		if err != nil {
			return EngineResult{
				Messages: copyMessages(messages[start:]),
				ModelRef: modelRef,
				Turn:     turn,
			}, fmt.Errorf("model complete: %w", err)
		}

		resultMessages := conversation.NormalizeMessages(copyMessages(result.Messages))
		toolCalls := resultToolCalls(resultMessages)
		reply := finalReply(resultMessages, extractor)
		if finalTurn && (len(toolCalls) > 0 ||
			(strings.TrimSpace(reply.Text) == "" && len(reply.MediaRefs) == 0)) {
			messages = append(messages, resultMessages...)
			for _, call := range toolCalls {
				messages = append(messages, toolResultMessage(call.ID, ToolResult{
					Output: "Tool call not executed: tools are disabled on the final turn.",
				}, true))
			}
			break
		}
		if len(toolCalls) == 0 && strings.TrimSpace(reply.Text) == "" &&
			len(reply.MediaRefs) == 0 &&
			emptyAssistantRetries < maxEmptyAssistantRetries {
			emptyAssistantRetries++
			continue
		}
		emptyAssistantRetries = 0

		messages = append(messages, resultMessages...)

		if len(toolCalls) == 0 {
			finalMessages := extractor.Canonicalize(messages[start:])
			reply = finalReply(finalMessages, extractor)
			var finalErr error
			if finalTurn && result.FinishReason != "" && result.FinishReason != "stop" {
				finalErr = fmt.Errorf(
					"final answer incomplete: provider finish reason %q",
					result.FinishReason,
				)
			}
			if strings.TrimSpace(reply.Text) == "" && len(reply.MediaRefs) == 0 {
				reply.Text = "(assistant returned no text)"
			}
			return EngineResult{
				Messages:    copyMessages(finalMessages),
				FinalText:   reply.Text,
				Attachments: conversation.CloneParts(reply.Attachments),
				MediaRefs:   append([]string(nil), reply.MediaRefs...),
				ModelRef:    modelRef,
				Turn:        turn,
			}, finalErr
		}

		for _, call := range toolCalls {
			if err := ctx.Err(); err != nil {
				return EngineResult{
					Messages: copyMessages(messages[start:]),
					ModelRef: modelRef,
					Turn:     turn,
				}, err
			}
			emitRunEvent(ctx, req.Observer, RunEvent{
				Type:     RunEventToolStarted,
				Turn:     turn,
				ModelRef: modelRef,
				ToolCall: call,
			})

			toolResult, err := e.runTool(ctx, toolRegistry, req.ToolCallPolicy, call)
			if err != nil {
				toolResult = ToolResult{Output: "tool error: " + err.Error()}
			}

			output := toolResult.Output

			emitRunEvent(ctx, req.Observer, RunEvent{
				Type:       RunEventToolFinished,
				Turn:       turn,
				ModelRef:   modelRef,
				ToolCall:   call,
				ToolOutput: output,
				Err:        err,
			})

			messages = append(messages, toolResultMessage(call.ID, toolResult, err != nil))
			if err := ctx.Err(); err != nil {
				return EngineResult{
					Messages: copyMessages(messages[start:]),
					ModelRef: modelRef,
					Turn:     turn,
				}, err
			}

			if assessment := loopDetector.Record(call, output); assessment.Critical {
				stopSummary := formatStopSummary(
					StopReasonToolLoopDetected,
					e.maxTurns,
					assessment.RepeatCount,
					assessment.NoProgressCount,
				)
				messages = append(messages, assistantTextMessage(stopSummary, ""))
				stopErr := &StopError{
					Reason: StopReasonToolLoopDetected,
					Detail: fmt.Sprintf(
						"tool loop detected (repeat=%d, no_progress=%d)",
						assessment.RepeatCount,
						assessment.NoProgressCount,
					),
				}
				finalMessages := extractor.Canonicalize(messages[start:])
				reply := finalReply(finalMessages, extractor)
				return EngineResult{
					Messages:    copyMessages(finalMessages),
					FinalText:   stopSummary,
					Attachments: conversation.CloneParts(reply.Attachments),
					MediaRefs:   append([]string(nil), reply.MediaRefs...),
					ModelRef:    modelRef,
					Turn:        turn,
				}, stopErr
			}
		}
	}

	stopReason, limit := StopReasonToolTurnLimit, e.maxTurns
	detail := fmt.Sprintf("max tool-call turns reached (%d)", limit)
	if req.TurnBudget != nil && req.TurnBudget.Remaining() == 0 {
		stopReason, limit = StopReasonTurnBudgetExhausted, req.TurnBudget.limit
		detail = fmt.Sprintf("turn budget exhausted (limit=%d)", limit)
	}
	stopSummary := formatStopSummary(
		stopReason,
		limit,
		loopDetector.MaxRepeatCount(),
		loopDetector.MaxNoProgressCount(),
	)
	messages = append(messages, assistantTextMessage(stopSummary, ""))
	stopErr := &StopError{
		Reason: stopReason,
		Detail: detail,
	}
	finalMessages := extractor.Canonicalize(messages[start:])
	reply := finalReply(finalMessages, extractor)
	return EngineResult{
		Messages:    copyMessages(finalMessages),
		FinalText:   stopSummary,
		Attachments: conversation.CloneParts(reply.Attachments),
		MediaRefs:   append([]string(nil), reply.MediaRefs...),
		ModelRef:    lastModelRef,
		Turn:        lastTurn,
	}, stopErr
}

func applySystemTextSource(
	ctx context.Context,
	messages []conversation.Message,
	source SystemTextSource,
) []conversation.Message {
	if source == nil {
		return messages
	}
	text := strings.TrimSpace(source(ctx))
	if text == "" {
		return messages
	}

	updated := systemMessage(text)
	for i := range messages {
		if messages[i].Role == conversation.SystemRole {
			messages[i] = updated
			return messages
		}
	}
	out := make([]conversation.Message, 0, len(messages)+1)
	out = append(out, updated)
	out = append(out, messages...)
	return out
}

func (e *Engine) runTool(
	ctx context.Context,
	tools ToolRegistry,
	policy ToolCallPolicy,
	call ToolCall,
) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	if tools == nil {
		return ToolResult{}, fmt.Errorf("no tool registry configured for call %q", call.Name)
	}
	if strings.TrimSpace(call.ID) == "" {
		return ToolResult{}, fmt.Errorf("tool call is missing id")
	}
	if policy != nil {
		if err := policy.CheckToolCall(call); err != nil {
			return ToolResult{}, err
		}
	}
	return tools.Run(ctx, call)
}

type modelFailureBudget struct {
	limit    int
	failures []ModelAttemptFailure
}

func (e *Engine) completeWithObserver(
	ctx context.Context,
	messages []conversation.Message,
	tools []ToolDefinition,
	requireToolCalling bool,
	turn int,
	observer RunObserver,
	budget *modelFailureBudget,
	contextFitter RequestContextFitter,
	generatedStart int,
	requestTail []conversation.Message,
) (string, ModelClientResult, error) {
	modelRefs := normalizeModelRefs(e.modelRefSource())
	if len(modelRefs) == 0 {
		return "", ModelClientResult{}, fmt.Errorf("no models configured")
	}
	inputBytes := 0
	if contextFitter == nil {
		messages = append(copyMessages(messages), copyMessages(requestTail)...)
		var err error
		inputBytes, err = RequestEncodedBytes(messages, tools)
		if err != nil {
			return "", ModelClientResult{}, err
		}
	}

	requirements := modelselection.InferRequirements(modelselection.Request{
		Messages:  messages,
		ToolCount: len(tools),
	})
	if requireToolCalling {
		requirements.ToolCalling = true
	}
	requirements.InputBytes = inputBytes

	plan, err := e.planner.Plan(modelRefs, requirements)
	if err != nil {
		return "", ModelClientResult{}, err
	}

	log.Printf(
		"q15: model selection turn=%d requirements=[%s] eligible=%v skipped=%s",
		turn,
		requirements.String(),
		plan.EligibleRefs,
		plan.SkipSummary(),
	)
	if len(plan.EligibleRefs) == 0 {
		return "", ModelClientResult{}, &modelselection.NoEligibleError{
			Requirements: requirements,
			Skipped:      append([]modelselection.Skip(nil), plan.Skipped...),
		}
	}

	attemptFailures := make([]ModelAttemptFailure, 0, len(plan.EligibleRefs))
	lastModelRef := ""
	for attempt, modelRef := range plan.EligibleRefs {
		if err := ctx.Err(); err != nil {
			return lastModelRef, ModelClientResult{}, err
		}
		lastModelRef = modelRef
		emitRunEvent(ctx, observer, RunEvent{
			Type:     RunEventModelTurnStarted,
			Turn:     turn,
			ModelRef: modelRef,
		})

		attemptMessages := messages
		if contextFitter != nil {
			modelContext := ModelContext{Ref: modelRef}
			if source, ok := e.modelClient.(ModelContextSource); ok {
				modelContext = source.ContextForModel(modelRef)
			}
			attemptMessages, err = contextFitter(
				ctx,
				modelContext,
				messages,
				tools,
				generatedStart,
				requestTail...)
			if err != nil {
				log.Printf(
					"q15: model turn=%d ref=%q attempt=%d/%d context fitting failed: %v",
					turn,
					modelRef,
					attempt+1,
					len(plan.EligibleRefs),
					err,
				)
				attemptFailures = append(
					attemptFailures,
					ModelAttemptFailure{ModelRef: modelRef, Err: err},
				)
				continue
			}
		}
		result, err := e.completeModel(ctx, modelRef, attemptMessages, tools, turn, observer)
		if ctx.Err() != nil {
			return modelRef, ModelClientResult{}, ctx.Err()
		}
		if err == nil {
			if contextFitter != nil && result.Usage.InputTokens > 0 {
				estimated, estimateErr := RequestEstimatedTokens(attemptMessages, tools)
				if estimateErr == nil {
					log.Printf(
						"q15: context usage ref=%q estimated_input_tokens=%d actual_input_tokens=%d output_tokens=%d estimator=%s",
						modelRef,
						estimated,
						result.Usage.InputTokens,
						result.Usage.OutputTokens,
						RequestTokenEstimator,
					)
				}
			}
			log.Printf(
				"q15: model turn=%d selected ref=%q attempt=%d/%d finish_reason=%q returned_messages=%d",
				turn,
				modelRef,
				attempt+1,
				len(plan.EligibleRefs),
				result.FinishReason,
				len(result.Messages),
			)
			return modelRef, result, nil
		}

		log.Printf(
			"q15: model turn=%d ref=%q attempt=%d/%d failed: %v",
			turn,
			modelRef,
			attempt+1,
			len(plan.EligibleRefs),
			err,
		)
		failure := ModelAttemptFailure{ModelRef: modelRef, Err: err}
		attemptFailures = append(attemptFailures, failure)
		if budget.limit > 0 {
			budget.failures = append(budget.failures, failure)
			if len(budget.failures) >= budget.limit {
				return modelRef, ModelClientResult{}, &ModelAttemptLimitError{
					Limit:           budget.limit,
					AttemptFailures: append([]ModelAttemptFailure(nil), budget.failures...),
				}
			}
		}
	}

	if len(attemptFailures) == 0 {
		return "", ModelClientResult{}, fmt.Errorf("no models configured")
	}
	return lastModelRef, ModelClientResult{}, &ModelFallbackError{
		EligibleRefs:    append([]string(nil), plan.EligibleRefs...),
		AttemptFailures: attemptFailures,
	}
}

func (e *Engine) completeModel(
	ctx context.Context,
	modelRef string,
	messages []conversation.Message,
	tools []ToolDefinition,
	turn int,
	observer RunObserver,
) (ModelClientResult, error) {
	if observer == nil {
		return e.modelClient.Complete(ctx, modelRef, messages, tools)
	}
	onDelta := func(eventType RunEventType) func(string) {
		return func(delta string) {
			if delta == "" || ctx.Err() != nil {
				return
			}
			emitRunEvent(ctx, observer, RunEvent{
				Type:     eventType,
				Turn:     turn,
				ModelRef: modelRef,
				Delta:    delta,
			})
		}
	}
	if client, ok := e.modelClient.(ReasoningStreamingModelClient); ok {
		return client.CompleteStreamWithReasoning(ctx, modelRef, messages, tools,
			onDelta(RunEventModelTurnDelta), onDelta(RunEventModelReasoningDelta))
	}
	if client, ok := e.modelClient.(StreamingModelClient); ok {
		return client.CompleteStream(
			ctx,
			modelRef,
			messages,
			tools,
			onDelta(RunEventModelTurnDelta),
		)
	}
	return e.modelClient.Complete(ctx, modelRef, messages, tools)
}

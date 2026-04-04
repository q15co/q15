package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

// Engine runs model/tool turns without assuming a user-facing chat turn or
// transcript persistence strategy.
type Engine struct {
	modelClient ModelClient
	planner     modelselection.Planner
	tools       ToolRegistry
	modelRefs   []string
	maxTurns    int
}

// EngineRequest describes one engine execution.
type EngineRequest struct {
	Messages           []conversation.Message
	UseTools           bool
	RequireToolCalling bool
	Observer           RunObserver
}

// EngineResult captures the engine-produced messages and final outcome for one
// execution.
type EngineResult struct {
	Messages  []conversation.Message
	FinalText string
	ModelRef  string
	Turn      int
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
	if planner == nil {
		planner = modelselection.Passthrough{}
	}
	return &Engine{
		modelClient: modelClient,
		planner:     planner,
		tools:       tools,
		modelRefs:   normalizeModelRefs(modelRefs),
		maxTurns:    defaultMaxTurns,
	}
}

// Run executes one model/tool cycle over the provided initial messages.
func (e *Engine) Run(ctx context.Context, req EngineRequest) (EngineResult, error) {
	messages := conversation.NormalizeMessages(copyMessages(req.Messages))
	start := len(messages)

	var toolDefs []ToolDefinition
	if req.UseTools && e.tools != nil {
		toolDefs = e.tools.Definitions()
	}

	loopDetector := newToolLoopDetector()
	emptyAssistantRetries := 0

	for turn := 0; turn < e.maxTurns; turn++ {
		requestMessages := copyMessages(messages)
		if emptyAssistantRetries > 0 {
			requestMessages = append(
				requestMessages,
				systemMessage(emptyResponseRetrySteeringPrompt),
			)
		}

		modelRef, result, err := e.completeWithObserver(
			ctx,
			requestMessages,
			toolDefs,
			req.RequireToolCalling && len(toolDefs) > 0,
			turn,
			req.Observer,
		)
		if err != nil {
			return EngineResult{
				Messages: copyMessages(messages[start:]),
				ModelRef: modelRef,
				Turn:     turn,
			}, fmt.Errorf("model complete: %w", err)
		}

		resultMessages := conversation.NormalizeMessages(copyMessages(result.Messages))
		toolCalls := resultToolCalls(resultMessages)
		answer := finalAnswer(resultMessages)
		if len(toolCalls) == 0 && answer == "" &&
			emptyAssistantRetries < maxEmptyAssistantRetries {
			emptyAssistantRetries++
			continue
		}
		emptyAssistantRetries = 0

		messages = append(messages, resultMessages...)

		if len(toolCalls) == 0 {
			answer = finalAnswer(messages[start:])
			if answer == "" {
				answer = "(assistant returned no text)"
			}
			return EngineResult{
				Messages:  copyMessages(messages[start:]),
				FinalText: answer,
				ModelRef:  modelRef,
				Turn:      turn,
			}, nil
		}

		for _, call := range toolCalls {
			emitRunEvent(ctx, req.Observer, RunEvent{
				Type:     RunEventToolStarted,
				Turn:     turn,
				ModelRef: modelRef,
				ToolCall: call,
			})

			toolResult, err := e.runTool(ctx, call)
			output := toolResult.Output
			if err != nil {
				output = "tool error: " + err.Error()
				toolResult = ToolResult{Output: output}
			}

			emitRunEvent(ctx, req.Observer, RunEvent{
				Type:       RunEventToolFinished,
				Turn:       turn,
				ModelRef:   modelRef,
				ToolCall:   call,
				ToolOutput: output,
				Err:        err,
			})

			messages = append(messages, toolResultMessage(call.ID, toolResult, err != nil))

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
				return EngineResult{
					Messages:  copyMessages(messages[start:]),
					FinalText: stopSummary,
					ModelRef:  modelRef,
					Turn:      turn,
				}, stopErr
			}
		}
	}

	stopSummary := formatStopSummary(
		StopReasonToolTurnLimit,
		e.maxTurns,
		loopDetector.MaxRepeatCount(),
		loopDetector.MaxNoProgressCount(),
	)
	messages = append(messages, assistantTextMessage(stopSummary, ""))
	stopErr := &StopError{
		Reason: StopReasonToolTurnLimit,
		Detail: fmt.Sprintf("max tool-call turns reached (%d)", e.maxTurns),
	}
	return EngineResult{
		Messages:  copyMessages(messages[start:]),
		FinalText: stopSummary,
	}, stopErr
}

func (e *Engine) runTool(ctx context.Context, call ToolCall) (ToolResult, error) {
	if e.tools == nil {
		return ToolResult{}, fmt.Errorf("no tool registry configured for call %q", call.Name)
	}
	if strings.TrimSpace(call.ID) == "" {
		return ToolResult{}, fmt.Errorf("tool call is missing id")
	}
	return e.tools.Run(ctx, call)
}

func (e *Engine) completeWithObserver(
	ctx context.Context,
	messages []conversation.Message,
	tools []ToolDefinition,
	requireToolCalling bool,
	turn int,
	observer RunObserver,
) (string, ModelClientResult, error) {
	if len(e.modelRefs) == 0 {
		return "", ModelClientResult{}, fmt.Errorf("no models configured")
	}

	requirements := modelselection.InferRequirements(modelselection.Request{
		Messages:  messages,
		ToolCount: len(tools),
	})
	if requireToolCalling {
		requirements.ToolCalling = true
	}

	plan, err := e.planner.Plan(e.modelRefs, requirements)
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

	var lastErr error
	lastModelRef := ""
	for attempt, modelRef := range plan.EligibleRefs {
		lastModelRef = modelRef
		emitRunEvent(ctx, observer, RunEvent{
			Type:     RunEventModelTurnStarted,
			Turn:     turn,
			ModelRef: modelRef,
		})

		result, err := e.modelClient.Complete(ctx, modelRef, messages, tools)
		if err == nil {
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
		lastErr = err
	}

	if lastErr == nil {
		return "", ModelClientResult{}, fmt.Errorf("no models configured")
	}
	return lastModelRef, ModelClientResult{}, fmt.Errorf(
		"all models failed (%v): %w",
		plan.EligibleRefs,
		lastErr,
	)
}

package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

type judgeFunc func(context.Context, string, []string) (Judgment, error)

func (f judgeFunc) Judge(ctx context.Context, state string, candidates []string) (Judgment, error) {
	return f(ctx, state, candidates)
}

func acceptCandidates(_ context.Context, _ string, candidates []string) (Judgment, error) {
	scores := make([]float64, len(candidates))
	for n := range scores {
		scores[n] = 1
	}
	return Judgment{Scores: scores}, nil
}

func TestJudgeHitsKeepsUncertaintyAndStableRanks(t *testing.T) {
	hits := []SearchHit{
		{TurnID: 3, Kind: "digest", Text: "first"},
		{TurnID: 1, Kind: "digest", Text: "second"},
		{TurnID: 2, Kind: "digest", Text: "third"},
	}
	for _, tc := range []struct {
		name   string
		scores []float64
		limit  int
		want   []int64
	}{
		{"nothing clears", []float64{0, 0.1, 0.29}, 3, nil},
		{"everything clears", []float64{0.6, 0.9, 0.8}, 3, []int64{1, 2, 3}},
		{"limit is a ceiling", []float64{0.1, 0.9, 0.05}, 3, []int64{1}},
		{"rank before slots", []float64{0.6, 0.9, 0.8}, 1, []int64{1}},
		{"ties retain fusion order", []float64{0.5, 0.5, 0.5}, 2, []int64{3, 1}},
		{"uncertainty survives score gap", []float64{1, 0.3, 0.29999}, 3, []int64{3, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := &ContextService{
				settings: ContextSettings{RetrievalLimit: tc.limit},
				judge: judgeFunc(
					func(_ context.Context, state string, candidates []string) (Judgment, error) {
						calls++
						if state != "current task" ||
							!reflect.DeepEqual(candidates, []string{"first", "second", "third"}) {
							t.Fatal("judge lost the state or fused candidate order")
						}
						return Judgment{Scores: tc.scores, InputTokens: 123}, nil
					},
				),
			}
			got, err := s.judgeHits(t.Context(), "current task", "raw", hits)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for _, hit := range got {
				ids = append(ids, hit.TurnID)
			}
			if !reflect.DeepEqual(ids, tc.want) || calls != 1 {
				t.Fatalf("selected=%v want=%v calls=%d", ids, tc.want, calls)
			}
			if hits[0].TurnID != 3 || hits[1].TurnID != 1 || hits[2].TurnID != 2 {
				t.Fatal("judging reordered the fallback pool")
			}
		})
	}
}

func TestJudgeFailurePreservesOriginalRetrievalAndLogsNoPayload(t *testing.T) {
	hits := []SearchHit{{TurnID: 7, Text: "private candidate"}, {TurnID: 4}, {TurnID: 9}}
	for _, tc := range []struct {
		name   string
		scores []float64
		err    error
	}{
		{name: "provider error", err: errors.New("private provider payload")},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "missing scores", scores: []float64{0.9, 0.8}},
		{name: "extra scores", scores: []float64{1, 1, 1, 1}},
		{name: "nan", scores: []float64{1, math.NaN(), 1}},
		{name: "infinity", scores: []float64{1, math.Inf(1), 1}},
		{name: "negative", scores: []float64{1, -0.1, 1}},
		{name: "above one", scores: []float64{1, 1.1, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			prior := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(prior) })
			s := &ContextService{
				settings: ContextSettings{RetrievalLimit: 2},
				judge: judgeFunc(func(context.Context, string, []string) (Judgment, error) {
					return Judgment{Scores: tc.scores}, tc.err
				}),
			}
			got, err := s.judgeHits(t.Context(), "private state", "raw", hits)
			if err != nil || !reflect.DeepEqual(got, hits[:2]) {
				t.Fatalf("fallback changed original hits: %+v %v", got, err)
			}
			if !strings.Contains(logs.String(), "retrieval_judge_fallback level=warn") ||
				strings.Contains(logs.String(), "private") {
				t.Fatalf("missing warning or leaked source: %s", &logs)
			}
		})
	}
}

func TestJudgeCapsQuestionsAndInputSpend(t *testing.T) {
	hits := make([]SearchHit, 100)
	for n := range hits {
		hits[n] = SearchHit{TurnID: int64(n + 1), Text: "small digest"}
	}
	candidates, estimated, err := judgeCandidates("task", hits)
	if err != nil || len(candidates) != 64 || estimated > maxJudgeTokens {
		t.Fatalf("question cap: count=%d estimate=%d error=%v", len(candidates), estimated, err)
	}
	for n := range hits {
		hits[n].Text = strings.Repeat("PostgreSQL audit result 1234.5678 requires evidence; ", 80)
	}
	candidates, estimated, err = judgeCandidates("task", hits)
	if err != nil || len(candidates) == 0 || len(candidates) >= 64 || estimated > maxJudgeTokens {
		t.Fatalf("input budget: count=%d estimate=%d error=%v", len(candidates), estimated, err)
	}
	tooLarge := strings.Repeat("PostgreSQL audit result 1234.5678 requires evidence; ", 10000)
	if _, _, err := judgeCandidates(tooLarge, hits); err == nil {
		t.Fatal("over-budget state was accepted")
	}
}

func TestJudgeEmptyPoolAndCallerCancellation(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithCancel(t.Context())
	s := &ContextService{
		settings: ContextSettings{RetrievalLimit: 2},
		judge: judgeFunc(func(ctx context.Context, _ string, _ []string) (Judgment, error) {
			calls++
			cancel()
			return Judgment{}, ctx.Err()
		}),
	}
	if got, err := s.judgeHits(ctx, "task", "none", nil); err != nil || len(got) != 0 ||
		calls != 0 {
		t.Fatalf("empty pool invoked judge: %v %v calls=%d", got, err, calls)
	}
	hits := []SearchHit{{TurnID: 1, Text: "digest"}}
	if _, err := s.judgeHits(ctx, "task", "none", hits); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation did not stop assembly: %v", err)
	}
	if _, err := s.judgeHits(ctx, "task", "none", hits); !errors.Is(err, context.Canceled) ||
		calls != 1 {
		t.Fatalf("canceled caller reached judge again: %v calls=%d", err, calls)
	}
}

func TestRetrievalJudgeStatePrefersDigestAndFallsBackToConversationText(t *testing.T) {
	user := strings.Repeat("é", 1000)
	assistant := strings.Repeat("界", 1000) + " Would you like me to apply the correction?"
	recent := []conversation.Turn{
		{Seq: 9, Messages: []conversation.Message{
			conversation.UserMessage(user),
			conversation.AssistantMessage(
				conversation.Text("private commentary", conversation.TextDispositionCommentary),
				conversation.Reasoning("private reasoning", nil),
				conversation.ToolCall("call", "read", `{"path":"private file"}`),
			),
			{
				Role: conversation.ToolRole,
				Parts: []conversation.Part{
					conversation.ToolResult("call", "private tool result", false),
				},
			},
			conversation.AssistantMessage(
				conversation.Text(assistant, conversation.TextDispositionFinal),
			),
		}},
		{
			Seq:      8,
			Messages: []conversation.Message{conversation.UserMessage("unrelated older topic")},
		},
	}
	input := []conversation.Message{conversation.UserMessage("yes")}
	state, source := retrievalJudgeState(
		input,
		recent,
		map[int64]string{9: "The correction was proposed.", 8: "unrelated older digest"},
	)
	if source != "digest" || !strings.Contains(state, "The correction was proposed.") ||
		strings.Contains(state, "previous_user_excerpt") || strings.Contains(state, "unrelated") {
		t.Fatalf("digest state = %s source=%s", state, source)
	}
	state, source = retrievalJudgeState(input, recent, nil)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(state), &decoded); err != nil {
		t.Fatal(err)
	}
	if source != "raw" || decoded["current_request"] != "yes" ||
		strings.Contains(state, "private") ||
		strings.Contains(state, "unrelated") ||
		!utf8.ValidString(state) ||
		utf8.RuneCountInString(decoded["previous_user_excerpt"].(string)) != 600 ||
		utf8.RuneCountInString(decoded["previous_assistant_tail"].(string)) != 800 ||
		!strings.HasSuffix(decoded["previous_assistant_tail"].(string), "apply the correction?") {
		t.Fatalf("raw state lost its conversation boundaries: %s source=%s", state, source)
	}
	state, source = retrievalJudgeState(input, nil, nil)
	if source != "none" || state != `{"current_request":"yes"}` {
		t.Fatalf("initial state = %s source=%s", state, source)
	}
}

func TestJudgeSeesContributingTurnBeforeRetrievalCut(t *testing.T) {
	dense := []historydb.SearchDenseRow{
		{
			TurnID: 10,
			UnitID: 10,
			Kind:   "digest",
			Body:   "Adjacent subsystem discussion",
			Score:  0.99,
		},
		{TurnID: 10, UnitID: 11, Kind: "chunk", Body: "Duplicate source", Score: 0.98},
		{
			TurnID: 5,
			UnitID: 5,
			Kind:   "digest",
			Body:   "Required audit retention is 90 days",
			Score:  0.8,
		},
	}
	pool := fuseHits(dense, nil, SearchOptions{Limit: 1})
	if len(pool) != 2 || pool[0].TurnID != 10 || pool[1].TurnID != 5 {
		t.Fatalf("fusion cut or failed to deduplicate candidates: %+v", pool)
	}
	s := &ContextService{
		settings: ContextSettings{RetrievalLimit: 1},
		judge: judgeFunc(func(context.Context, string, []string) (Judgment, error) {
			return Judgment{Scores: []float64{0.01, 0.95}}, nil
		}),
	}
	got, err := s.judgeHits(t.Context(), "audit retention question", "digest", pool)
	if err != nil || len(got) != 1 || got[0].TurnID != 5 {
		t.Fatalf("lower-ranked contributing turn was lost: %+v %v", got, err)
	}
}

func TestContextJudgesOnceAndReusesSelectionDuringToolSteps(t *testing.T) {
	store := testStore(t)
	contributing := appendTestTurn(t, store, "PostgreSQL requires audit retention of 90 days.")
	adjacent := appendTestTurn(t, store, "PostgreSQL subsystem meeting.")
	appendTestTurn(t, store, "Latest conversation stays in recent context.")
	indexer, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{
			Provider:   "fixture",
			Model:      "judged-history",
			Dimensions: 3,
		},
		&testEmbedder{},
		DigestFunc(func(_ context.Context, source string) (DigestResult, error) {
			target := source[strings.LastIndex(source, "\nTARGET turn "):]
			body := "Latest conversation."
			if strings.Contains(target, "90 days") {
				body = "PostgreSQL requires audit retention of 90 days."
			} else if strings.Contains(target, "subsystem meeting") {
				body = "PostgreSQL subsystem meeting."
			}
			return DigestResult{Body: body, Provider: "fixture", Model: "summary"}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.RunBatch(t.Context(), 32); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service, err := NewContextService(indexer, ContextSettings{RecentTurns: 1, RetrievalLimit: 1},
		judgeFunc(func(_ context.Context, state string, candidates []string) (Judgment, error) {
			calls++
			if len(candidates) != 2 || !strings.Contains(state, "Latest conversation.") {
				t.Fatalf(
					"judge did not receive the pool and previous digest: %v %s",
					candidates,
					state,
				)
			}
			scores := make([]float64, len(candidates))
			for n, candidate := range candidates {
				if strings.Contains(candidate, "90 days") {
					scores[n] = 0.9
				}
			}
			return Judgment{Scores: scores}, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareContext(
		t.Context(),
		[]conversation.Message{conversation.SystemMessage("Instructions")},
		[]conversation.Message{conversation.UserMessage("PostgreSQL")},
	)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, message := range prepared.Messages {
		text.WriteString(conversation.TextValue(message))
	}
	if !strings.Contains(text.String(), "90 days") ||
		strings.Contains(text.String(), "subsystem meeting") {
		t.Fatalf("wrong context injected: %s", &text)
	}
	for range 2 {
		if _, err := prepared.FitRequest(t.Context(), agent.ModelContext{MaxContextTokens: 32000}, prepared.Messages, nil, len(prepared.Messages)); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("tool-step fitting repeated judgment: %d calls", calls)
	}
	manual, err := indexer.Search(
		t.Context(),
		"PostgreSQL",
		SearchOptions{Limit: 1, SummariesOnly: true},
	)
	if err != nil || len(manual.Hits) != 1 || manual.Hits[0].TurnID != adjacent {
		t.Fatalf(
			"manual retrieval ranking changed: %+v %v (contributing turn %d)",
			manual,
			err,
			contributing,
		)
	}
}

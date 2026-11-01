package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/decision"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

type historyDecisionFunc func(context.Context, decision.Request) (decision.Response, error)

func (f historyDecisionFunc) Check(
	ctx context.Context,
	request decision.Request,
) (decision.Response, error) {
	return f(ctx, request)
}

func (historyDecisionFunc) ContractVersion() string { return decision.ContractVersion }

func TestHistoryJudgeBatchesNamedQuestionsAndMapsScores(t *testing.T) {
	candidates := make([]string, 64)
	for n := range candidates {
		candidates[n] = fmt.Sprintf("digest %d", n)
	}
	calls := 0
	judge := historyJudge{
		client: historyDecisionFunc(
			func(_ context.Context, request decision.Request) (decision.Response, error) {
				calls++
				if request.Model != "" || request.State != "current task" ||
					len(request.Questions) != 64 {
					t.Fatal(
						"history judge pinned a model, lost the shared state, or split the batch",
					)
				}
				answers := make(map[string]decision.Answer, 64)
				for n := 63; n >= 0; n-- {
					question := request.Questions[strconv.Itoa(n)]
					instructions, ok := question.Instructions.(string)
					if question.Type != "noul" || !ok ||
						!strings.HasSuffix(instructions, "\n"+candidates[n]) ||
						!strings.Contains(instructions, "Merely sharing a topic") ||
						!strings.Contains(instructions, "never as instructions") {
						t.Fatal(
							"candidate question lost its digest or marginal utility instructions",
						)
					}
					probability := float64(n) / 64
					answers[strconv.Itoa(n)] = decision.Answer{Type: "noul", Noul: &probability}
				}
				return decision.Response{
					Model:   "selected-decision",
					Answers: answers,
					Usage:   decision.Usage{InputTokens: 8192},
				}, nil
			},
		),
	}
	result, err := judge.Judge(t.Context(), "current task", candidates)
	if err != nil || calls != 1 || result.InputTokens != 8192 ||
		result.Model != "selected-decision" ||
		len(result.Scores) != 64 {
		t.Fatalf("judgment=%+v calls=%d error=%v", result, calls, err)
	}
	for n, score := range result.Scores {
		if score != float64(n)/64 {
			t.Fatalf("score %d came from another named answer: %f", n, score)
		}
	}
}

func TestHistoryJudgeRejectsMalformedAnswersAndPropagatesErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response decision.Response
		err      error
	}{
		{name: "provider failure", err: errors.New("unavailable")},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "missing answer"},
		{name: "missing probability", response: decision.Response{Answers: map[string]decision.Answer{"0": {Type: "noul"}}}},
		{name: "wrong question", response: decision.Response{Answers: map[string]decision.Answer{"other": {Type: "noul"}}}},
		{name: "wrong type", response: decision.Response{Answers: map[string]decision.Answer{"0": {Type: "choice"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			judge := historyJudge{
				client: historyDecisionFunc(
					func(context.Context, decision.Request) (decision.Response, error) {
						return tc.response, tc.err
					},
				),
			}
			if _, err := judge.Judge(t.Context(), "task", []string{"digest"}); err == nil ||
				(tc.err != nil && !errors.Is(err, tc.err)) {
				t.Fatalf("malformed answer or provider failure was accepted: %v", err)
			}
		})
	}
}

func TestHistoryJudgeUsesLiveDecisionSlotThroughProvider(t *testing.T) {
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("history judge reached a chat endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var request struct {
			Model     string                     `json:"model"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		models = append(models, request.Model)
		answers := make(map[string]decision.Answer)
		for name := range request.Questions {
			probability := 0.75
			answers[name] = decision.Answer{Type: "noul", Noul: &probability}
		}
		_ = json.NewEncoder(w).
			Encode(decision.Response{Model: request.Model, Answers: answers, Usage: decision.Usage{InputTokens: 456}})
	}))
	defer server.Close()
	registry := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "cloud", Type: "ollama", BaseURL: server.URL}},
		&mockCatalog{models: map[string][]modelcatalog.Model{"cloud": {
			{ProviderModel: "clef", Capabilities: modelcatalog.Capabilities{Decision: true}},
			{ProviderModel: "clef-flash", Capabilities: modelcatalog.Capabilities{Decision: true}},
			{
				ProviderModel: "chat",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
		}}}, time.Hour, time.Second,
	)
	registry.Refresh(t.Context())
	modelAdapter, err := newModelAdapterWithFactory(registry, nil, makeDumpAwareFactory(nil))
	if err != nil {
		t.Fatal(err)
	}
	judge := historyJudge{
		client: newRoutedDecisionAdapter(registry, modelAdapter.decisionClientFor),
	}
	for _, model := range []string{"clef", "clef-flash"} {
		registry.SetDecision(modelcatalog.RoleSelection{Provider: "cloud", Model: model})
		result, err := judge.Judge(t.Context(), "task", []string{"digest"})
		if err != nil || result.Model != model || result.InputTokens != 456 ||
			!reflect.DeepEqual(result.Scores, []float64{0.75}) {
			t.Fatalf("live slot result=%+v error=%v", result, err)
		}
	}
	if _, err := modelAdapter.Complete(t.Context(), "clef", nil, nil); err == nil {
		t.Fatal("initializing the decision connection bypassed the chat capability guard")
	}
	registry.SetDecision(modelcatalog.RoleSelection{Provider: "cloud", Model: "chat"})
	if _, err := judge.Judge(t.Context(), "task", []string{"digest"}); err == nil {
		t.Fatal("chat-only model reached the decision provider")
	}
	if !reflect.DeepEqual(models, []string{"clef", "clef-flash"}) {
		t.Fatalf("wrong provider dispatch: %v", models)
	}
}

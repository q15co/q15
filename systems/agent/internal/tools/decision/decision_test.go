package decision

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/decision"
)

type decisionClientFunc func(context.Context, decision.Request) (decision.Response, error)

func (f decisionClientFunc) Check(
	ctx context.Context,
	request decision.Request,
) (decision.Response, error) {
	return f(ctx, request)
}

func TestDecisionCheckUsesInjectedClient(t *testing.T) {
	calls := 0
	client := decisionClientFunc(
		func(_ context.Context, request decision.Request) (decision.Response, error) {
			calls++
			if request.Model != "" ||
				string(request.State.(json.RawMessage)) != `{"ticket":"context"}` ||
				request.Questions["check"].Options[0].Name != "first" {
				t.Errorf("request = %+v", request)
			}
			confidence := 0.5
			return decision.Response{
				Answers: map[string]decision.Answer{
					"check": {
						Type:          "choice",
						Choice:        "first",
						Probabilities: decision.Probabilities{"first": 0.8, "second": 0.2},
						Confidence:    &confidence,
					},
				},
			}, nil
		},
	)
	tool := NewCheck(client)
	out, err := tool.Run(
		context.Background(),
		`{"state":{"ticket":"context"},"question":"What next?","options":["first","second"]}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	var answer decision.Answer
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Choice != "first" || answer.Probabilities["first"] != 0.8 {
		t.Fatalf("answer = %s", out)
	}
	for _, args := range []string{`{}`, `{"state":"context","question":"Pick?","options":["only"]}`, `{"state":"context","question":"Pick?","options":["same","same"]}`} {
		if _, err := tool.Run(context.Background(), args); err == nil {
			t.Errorf("accepted invalid arguments %s", args)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid arguments reached client: %d calls", calls)
	}
}

func (f decisionClientFunc) ContractVersion() string { return decision.ContractVersion }

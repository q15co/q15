package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/decision"
)

func TestClientSystemOneQuestionTypesAndBatch(t *testing.T) {
	for _, state := range []any{"original state", map[string]any{"ticket": "original state"}, []any{"one", "two"}} {
		t.Run(fmt.Sprintf("%T", state), func(t *testing.T) {
			questions := map[string]decision.Question{
				"route": decision.Choice(
					"Route?",
					[]decision.Option{
						{Name: "z-first"},
						{Name: "a-second", Description: "Fallback"},
					},
				),
				"refund":  decision.Noul("Refund?"),
				"urgency": decision.Score("Urgency?", []string{"Routine", "Soon", "Immediate"}),
			}
			for i := 3; i < 64; i++ {
				questions[fmt.Sprintf("q%02d", i)] = decision.Noul("True?")
			}
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
						t.Errorf("endpoint = %s %s", r.Method, r.URL.Path)
					}
					if r.Header.Get("Authorization") != "Bearer test-key" ||
						r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("headers = %v", r.Header)
					}
					body, _ := io.ReadAll(r.Body)
					var request struct {
						Model     string                     `json:"model"`
						State     json.RawMessage            `json:"state"`
						Questions map[string]json.RawMessage `json:"questions"`
						Images    []string                   `json:"images"`
						KeepAlive string                     `json:"keep_alive"`
					}
					if err := json.Unmarshal(body, &request); err != nil {
						t.Error(err)
						http.Error(w, "bad request", http.StatusBadRequest)
						return
					}
					wantState, _ := json.Marshal(state)
					if request.Model != "clef:27b" || len(request.Questions) != 64 ||
						!bytes.Equal(request.State, wantState) ||
						request.KeepAlive != "5m" ||
						len(request.Images) != 1 ||
						request.Images[0] != "aW1hZ2U=" {
						t.Errorf("unexpected request: %s", body)
					}
					if !bytes.Contains(
						request.Questions["route"],
						[]byte(`"criteria":{"z-first":null,"a-second":"Fallback"}`),
					) {
						t.Errorf("criteria lost option order: %s", request.Questions["route"])
					}
					if !bytes.Contains(
						request.Questions["urgency"],
						[]byte(`"criteria":["Routine","Soon","Immediate"]`),
					) {
						t.Errorf("score criteria = %s", request.Questions["urgency"])
					}
					answers := map[string]any{
						"route": map[string]any{
							"type":          "choice",
							"choice":        "a-second",
							"probabilities": map[string]float64{"z-first": 0.5, "a-second": 0.5},
							"confidence":    0,
						},
						"refund": map[string]any{"type": "noul", "noul": 0.95},
						"urgency": map[string]any{
							"type":  "score",
							"score": 1.5,
							"legend": map[string]string{
								"0": "Routine",
								"1": "Soon",
								"2": "Immediate",
							},
							"probabilities": map[string]float64{"0": 0.1, "1": 0.3, "2": 0.6},
							"confidence":    0.2,
						},
					}
					for i := 3; i < 64; i++ {
						answers[fmt.Sprintf("q%02d", i)] = map[string]any{
							"type": "noul",
							"noul": float64(i) / 100,
						}
					}
					_ = json.NewEncoder(w).
						Encode(map[string]any{"model": "clef:27b", "answers": answers, "usage": map[string]int{"input_tokens": 120, "output_tokens": 64}})
				}),
			)
			defer server.Close()
			client, err := NewClient(server.URL+"/v1/", "test-key", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Check(
				context.Background(),
				decision.Request{
					Model:     "clef:27b",
					State:     state,
					Questions: questions,
					Images:    []string{"aW1hZ2U="},
					KeepAlive: "5m",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Answers) != 64 || result.Answers["route"].Choice != "z-first" ||
				*result.Answers["route"].Confidence != 0 ||
				*result.Answers["refund"].Noul != 0.95 ||
				*result.Answers["urgency"].Score != 1.5 ||
				result.Answers["urgency"].Legend["2"] != "Immediate" ||
				result.Usage.InputTokens != 120 {
				t.Fatalf("unexpected result: %+v", result)
			}
			for i := 3; i < 64; i++ {
				if got := *result.Answers[fmt.Sprintf("q%02d", i)].Noul; got != float64(i)/100 {
					t.Errorf("answer %d = %v", i, got)
				}
			}
		})
	}
}

func TestClientErrorsNameEndpointWithoutPayload(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"status", 502, `private-state private-key`},
		{"invalid-json", 200, `private-state private-key`},
		{"null", 200, `null`},
		{"missing-answer", 200, `{"answers":{}}`},
		{"wrong-type", 200, `{"answers":{"check":{"type":"score","score":1}}}`},
		{"unknown-option", 200, `{"answers":{"check":{"type":"choice","choice":"other","probabilities":{"yes":0.9,"no":0.1},"confidence":0.5}}}`},
		{"missing-confidence", 200, `{"answers":{"check":{"type":"choice","choice":"yes","probabilities":{"yes":0.9,"no":0.1}}}}`},
		{"bad-probability", 200, `{"answers":{"check":{"type":"choice","choice":"yes","probabilities":{"yes":2,"no":-1},"confidence":0.5}}}`},
		{"missing-probability", 200, `{"answers":{"check":{"type":"choice","choice":"yes","probabilities":{"yes":1},"confidence":0.5}}}`},
		{"null-probability", 200, `{"answers":{"check":{"type":"choice","choice":"yes","probabilities":{"yes":1,"no":null},"confidence":0.5}}}`},
		{"bad-sum", 200, `{"answers":{"check":{"type":"choice","choice":"yes","probabilities":{"yes":0.3,"no":0.1},"confidence":0.5}}}`},
		{"wrong-name", 200, `{"answers":{"different":{"type":"choice","choice":"yes","probabilities":{"yes":0.9,"no":0.1},"confidence":0.5}}}`},
		{"trailing-json", 200, `{"answers":{"check":{"type":"choice","choice":"yes","probabilities":{"yes":0.9,"no":0.1},"confidence":0.5}}} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}),
			)
			defer server.Close()
			client, err := NewClient(server.URL+"/v1", "private-key", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Check(
				context.Background(),
				decision.Request{
					Model: "clef:27b",
					State: "private-state",
					Questions: map[string]decision.Question{
						"check": decision.Choice(
							"Choose?",
							[]decision.Option{{Name: "yes"}, {Name: "no"}},
						),
					},
				},
			)
			if err == nil || !strings.Contains(err.Error(), "/v1/systemone") {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "private-state") ||
				strings.Contains(err.Error(), "private-key") {
				t.Fatalf("payload in error: %v", err)
			}
		})
	}
	if logs.Len() != 0 {
		t.Fatalf("client logged payload: %s", logs.String())
	}
}

func TestClientVersionedBaseURLAndZeroValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/v1/systemone" || r.Header.Get("Authorization") != "" {
			t.Errorf("request = %s, headers %v", r.URL.Path, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"keep_alive":0`)) ||
			!bytes.Contains(body, []byte(`"criteria":{"true":"Present","false":"Absent"}`)) {
			t.Errorf("body = %s", body)
		}
		_, _ = io.WriteString(w, `{"answers":{"zero":{"type":"noul","noul":0}}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL+"/gateway/v1/", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	q := decision.Noul("Present?")
	q.NoulCriteria = &decision.NoulCriteria{True: "Present", False: "Absent"}
	result, err := client.Check(
		context.Background(),
		decision.Request{
			Model:     "clef",
			State:     "",
			Questions: map[string]decision.Question{"zero": q},
			KeepAlive: 0,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Answers["zero"].Noul == nil || *result.Answers["zero"].Noul != 0 {
		t.Fatalf("answer = %+v", result.Answers["zero"])
	}
}

func TestClientRejectsMalformedNoulAndScore(t *testing.T) {
	for _, tc := range []struct {
		name     string
		question decision.Question
		answer   string
	}{
		{"noul-missing", decision.Noul("True?"), `{"type":"noul"}`},
		{"noul-out-of-range", decision.Noul("True?"), `{"type":"noul","noul":2}`},
		{"score-missing", decision.Score("Rank?", []string{"low", "high"}), `{"type":"score","probabilities":{"0":0.5,"1":0.5},"confidence":0,"legend":{"0":"low","1":"high"}}`},
		{"score-out-of-range", decision.Score("Rank?", []string{"low", "high"}), `{"type":"score","score":2,"probabilities":{"0":0.5,"1":0.5},"confidence":0,"legend":{"0":"low","1":"high"}}`},
		{"score-wrong-legend", decision.Score("Rank?", []string{"low", "high"}), `{"type":"score","score":0.5,"probabilities":{"0":0.5,"1":0.5},"confidence":0,"legend":{"0":"high","1":"low"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = fmt.Fprintf(w, `{"answers":{"check":%s}}`, tc.answer)
				}),
			)
			defer server.Close()
			client, err := NewClient(server.URL+"/v1", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Check(context.Background(), decision.Request{Model: "clef", State: "state", Questions: map[string]decision.Question{"check": tc.question}}); err == nil ||
				!strings.Contains(err.Error(), "/v1/systemone") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestClientRejectsInvalidRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := NewClient(server.URL+"/v1", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	many := map[string]decision.Question{}
	for i := 0; i < 65; i++ {
		many[fmt.Sprint(i)] = decision.Noul("True?")
	}
	for _, req := range []decision.Request{
		{State: "state", Questions: map[string]decision.Question{"q": decision.Noul("True?")}},
		{Model: "clef", Questions: map[string]decision.Question{"q": decision.Noul("True?")}},
		{Model: "clef", State: true, Questions: map[string]decision.Question{"q": decision.Noul("True?")}},
		{Model: "clef", State: "state"},
		{Model: "clef", State: "state", Questions: many},
		{Model: "clef", State: "state", Questions: map[string]decision.Question{"q": decision.Choice("Pick?", []decision.Option{{Name: "one"}})}},
		{Model: "clef", State: "state", Questions: map[string]decision.Question{"q": decision.Choice("Pick?", []decision.Option{{Name: "one"}, {Name: "one"}})}},
		{Model: "clef", State: "state", Questions: map[string]decision.Question{"q": decision.Score("Rank?", []string{"only"})}},
	} {
		if _, err := client.Check(context.Background(), req); err == nil ||
			!strings.Contains(err.Error(), "/v1/systemone") {
			t.Errorf("request accepted: %+v, error %v", req, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached the provider: %d calls", calls.Load())
	}
}

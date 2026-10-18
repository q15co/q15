package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ollamaapi "github.com/ollama/ollama/api"
	"github.com/q15co/q15/systems/agent/internal/decision"
)

func TestOllamaDecisionSDKPreservesTypedBatch(t *testing.T) {
	questions := map[string]decision.Question{
		"route": decision.Choice(
			"Route?",
			[]decision.Option{{Name: "z-first"}, {Name: "a-second"}},
		),
		"refund":  decision.Noul("Refund?"),
		"urgency": decision.Score("Urgency?", []string{"Routine", "Soon", "Immediate"}),
	}
	for i := 3; i < 64; i++ {
		questions[fmt.Sprintf("q%02d", i)] = decision.Noul("True?")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost ||
			!strings.HasPrefix(r.UserAgent(), "ollama/") {
			t.Errorf("request = %s %s, user agent %q", r.Method, r.URL.Path, r.UserAgent())
		}
		var request ollamaapi.SystemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if request.Model != "clef:27b" || string(request.State) != `{"ticket":"context"}` ||
			request.Questions.Len() != 64 ||
			request.KeepAlive == nil ||
			request.KeepAlive.Duration != 5*time.Minute ||
			len(request.Images) != 1 ||
			string(request.Images[0]) != "image" {
			t.Errorf("request = %+v", request)
		}
		route, _ := request.Questions.Get("route")
		if string(route.Criteria) != `{"z-first":null,"a-second":null}` {
			t.Errorf("criteria lost option order: %s", route.Criteria)
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
				"type":          "score",
				"score":         1.5,
				"legend":        map[string]string{"0": "Routine", "1": "Soon", "2": "Immediate"},
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
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "", nil, server.Client().Transport, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Check(
		context.Background(),
		decision.Request{
			Model:     "clef:27b",
			State:     map[string]string{"ticket": "context"},
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
		result.Usage.InputTokens != 120 ||
		result.Usage.OutputTokens != 64 {
		t.Fatalf("response = %+v", result)
	}
	for i := 3; i < 64; i++ {
		if got := *result.Answers[fmt.Sprintf("q%02d", i)].Noul; got != float64(i)/100 {
			t.Errorf("answer %d = %v", i, got)
		}
	}
}

type sdkDecisionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sdkDecisionRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestOllamaDecisionSDKCloudUsesBearerKey(t *testing.T) {
	t.Setenv("OLLAMA_AUTH", "false")
	calls := 0
	transport := sdkDecisionRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.String() != "https://ollama.com/prefix/v1/systemone" ||
			request.Header.Get("Authorization") != "Bearer test-key" ||
			!strings.HasPrefix(request.UserAgent(), "ollama/") {
			t.Errorf("request = %s, headers %v", request.URL, request.Header)
		}
		return &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body: io.NopCloser(
				strings.NewReader(`{"answers":{"check":{"type":"noul","noul":0}}}`),
			),
		}, nil
	})
	client, err := NewClient("https://ollama.com/prefix/api", "test-key", nil, transport, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Check(
		context.Background(),
		decision.Request{
			Model:     "clef:27b",
			State:     "context",
			Questions: map[string]decision.Question{"check": decision.Noul("True?")},
			KeepAlive: 0,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Answers["check"].Noul == nil || *result.Answers["check"].Noul != 0 {
		t.Fatalf("calls = %d, response = %+v", calls, result)
	}
}

func TestOllamaDecisionSDKErrorsOmitPayload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"redirect", 302, `{"answers":{"check":{"type":"noul","noul":1}}}`},
		{"unauthorized", 401, `{"error":"private-payload private-key"}`},
		{"upstream", 502, `private-payload private-key`},
		{"malformed", 200, `private-payload private-key`},
		{"missing", 200, `{"answers":{}}`},
		{"out-of-range", 200, `{"answers":{"check":{"type":"noul","noul":2}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}),
			)
			defer server.Close()
			client, err := NewClient(
				server.URL,
				"private-key",
				nil,
				server.Client().Transport,
				"",
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Check(
				context.Background(),
				decision.Request{
					Model:     "clef",
					State:     "private-payload",
					Questions: map[string]decision.Question{"check": decision.Noul("True?")},
				},
			)
			if err == nil || !strings.Contains(err.Error(), "/v1/systemone") ||
				strings.Contains(err.Error(), "private-payload") ||
				strings.Contains(err.Error(), "private-key") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestOllamaDecisionSDKPreservesCancellation(t *testing.T) {
	client, err := NewClient("http://unused.invalid", "", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Check(
		ctx,
		decision.Request{
			Model:     "clef",
			State:     "context",
			Questions: map[string]decision.Question{"check": decision.Noul("True?")},
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	transport := sdkDecisionRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		cancel()
		return nil, request.Context().Err()
	})
	client, err = NewClient("https://ollama.com", "test-key", nil, transport, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Check(
		ctx,
		decision.Request{
			Model:     "clef",
			State:     "context",
			Questions: map[string]decision.Question{"check": decision.Noul("True?")},
		},
	)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "q15-ollama.invalid") {
		t.Fatalf("in-flight cancellation = %v", err)
	}
}

func TestOllamaDecisionConfigurablePath(t *testing.T) {
	for _, tc := range []struct{ path, want string }{{"", "/prefix/v1/systemone"}, {"typed", "/prefix/typed"}, {"/custom/decide", "/custom/decide"}} {
		t.Run(tc.path, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != tc.want || !strings.HasPrefix(r.UserAgent(), "ollama/") {
						t.Errorf("SDK request = %s %s", r.URL.Path, r.UserAgent())
					}
					_, _ = io.WriteString(w, `{"answers":{"check":{"type":"noul","noul":0}}}`)
				}),
			)
			defer server.Close()
			client, err := NewClient(
				server.URL+"/prefix/api",
				"",
				nil,
				server.Client().Transport,
				tc.path,
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Check(
				context.Background(),
				decision.Request{
					Model:     "model",
					State:     "state",
					Questions: map[string]decision.Question{"check": decision.Noul("True?")},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOllamaClientSharesDecisionTransportWithoutChatDeadline(t *testing.T) {
	var deadlines []bool
	transport := sdkDecisionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, deadline := r.Context().Deadline()
		deadlines = append(deadlines, deadline)
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Error("missing shared bearer key")
		}
		body := `{"message":{"role":"assistant","content":"ok"},"done":true}`
		if strings.HasSuffix(r.URL.Path, "/v1/systemone") {
			body = `{"answers":{"check":{"type":"noul","noul":0}}}`
		}
		return &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	client, err := NewClient("https://ollama.com", " key ", nil, transport, "")
	if err != nil {
		t.Fatal(err)
	}
	var port decision.Client = client
	if _, err = port.Check(context.Background(), decision.Request{Model: "both", State: "context", Questions: map[string]decision.Question{"check": decision.Noul("True?")}}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Complete(context.Background(), "both", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(deadlines) != 2 || !deadlines[0] || deadlines[1] {
		t.Fatalf("decision/chat deadlines = %v", deadlines)
	}
}

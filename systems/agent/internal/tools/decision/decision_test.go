package decision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/discovery"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	modeltools "github.com/q15co/q15/systems/agent/internal/tools/models"
)

func TestDecisionCheckThroughOllamaRoster(t *testing.T) {
	for _, model := range []string{"clef:27b", "clef-flash"} {
		t.Run(model, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer tool-key" {
						t.Errorf("authorization missing")
					}
					switch r.URL.Path {
					case "/api/tags":
						_ = json.NewEncoder(w).
							Encode(map[string]any{"models": []any{map[string]string{"name": model}}})
					case "/api/show":
						_, _ = io.WriteString(w, `{"capabilities":["decision"]}`)
					case "/v1/systemone":
						calls++
						body, _ := io.ReadAll(r.Body)
						var req struct {
							Model     string          `json:"model"`
							State     json.RawMessage `json:"state"`
							Questions map[string]struct {
								Type         string          `json:"type"`
								Instructions string          `json:"instructions"`
								Criteria     json.RawMessage `json:"criteria"`
							} `json:"questions"`
						}
						if err := json.Unmarshal(body, &req); err != nil {
							t.Error(err)
							http.Error(w, "bad request", 400)
							return
						}
						q := req.Questions["check"]
						if req.Model != model || string(req.State) != `{"ticket":"context"}` ||
							q.Type != "choice" ||
							q.Instructions != "What next?" ||
							string(q.Criteria) != `{"z-preferred":null,"a-fallback":null}` {
							t.Errorf("request = %s", body)
						}
						_, _ = io.WriteString(
							w,
							`{"model":"clef","answers":{"check":{"type":"choice","choice":"z-preferred","probabilities":{"z-preferred":0.5,"a-fallback":0.5},"confidence":0}}}`,
						)
					default:
						http.NotFound(w, r)
					}
				}),
			)
			defer server.Close()
			registry := modelcatalog.New(
				[]modelcatalog.Provider{
					{Name: "ollama-cloud", Type: "ollama", BaseURL: server.URL, APIKey: "tool-key"},
				},
				discovery.NewCatalog(server.Client()),
				time.Hour,
				time.Second,
			)
			registry.Refresh(context.Background())
			roster, err := modeltools.NewListModels(registry, nil).
				Run(context.Background(), `{"provider":"ollama-cloud"}`)
			if err != nil || !strings.Contains(roster, `"provider_model": "`+model+`"`) {
				t.Fatalf("list_models = %s, %v", roster, err)
			}
			tool := NewCheck(registry)
			if tool.Definition().Name != "decision_check" {
				t.Fatalf("definition = %+v", tool.Definition())
			}
			out, err := tool.Run(
				context.Background(),
				`{"state":{"ticket":"context"},"question":"What next?","options":["z-preferred","a-fallback"]}`,
			)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Choice        string             `json:"choice"`
				Probabilities map[string]float64 `json:"probabilities"`
				Confidence    float64            `json:"confidence"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result.Choice != "z-preferred" ||
				result.Probabilities["z-preferred"] != 0.5 ||
				result.Probabilities["a-fallback"] != 0.5 ||
				result.Confidence != 0 {
				t.Fatalf("calls = %d, output = %s", calls, out)
			}
			for _, args := range []string{`{`, `{}`, `{"state":"context","question":"Pick?","options":["only"]}`, `{"state":"context","question":"Pick?","options":["same","same"]}`} {
				if _, err := tool.Run(context.Background(), args); err == nil {
					t.Errorf("accepted %s", args)
				}
			}
			if calls != 1 {
				t.Fatalf("invalid arguments reached endpoint: %d calls", calls)
			}
		})
	}
}

func TestDecisionCheckUnavailable(t *testing.T) {
	if _, err := NewCheck(nil).Run(context.Background(), `{"state":"context","question":"Pick?","options":["one","two"]}`); err == nil {
		t.Fatal("missing registry accepted")
	}
	registry := modelcatalog.New(nil, nil, time.Hour, time.Second)
	if _, err := NewCheck(registry).Run(context.Background(), `{"state":"context","question":"Pick?","options":["one","two"]}`); err == nil {
		t.Fatal("missing decision model accepted")
	}
}

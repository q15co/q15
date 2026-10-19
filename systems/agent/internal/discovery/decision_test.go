package discovery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	modeltools "github.com/q15co/q15/systems/agent/internal/tools/models"
)

func TestCatalogDiscoversDecisionModelsFromModelsDev(t *testing.T) {
	newCacheDir(t)
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "all" {
			_, _ = io.WriteString(w, `{"gateway":{"models":{}}}`)
			return
		}
		_, _ = io.WriteString(
			w,
			`{"gateway":{"models":{"vendor/new-model":{"type":"decision","name":"New Decision Model","modalities":{"input":["text"]},"limit":{"context":65536}},"not-in-live-roster":{"type":"decision","name":"Unavailable"}}}}`,
		)
	}))
	defer metadata.Close()
	old := defaultCatalogModelsDevURL
	defaultCatalogModelsDevURL = metadata.URL
	t.Cleanup(func() { defaultCatalogModelsDevURL = old })
	roster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"vendor/new-model"},{"id":"chat"}]}`)
	}))
	defer roster.Close()
	registry := modelcatalog.New(
		[]Provider{
			{
				Name:    "gateway",
				Type:    "openai-compatible",
				BaseURL: roster.URL + "/gateway/v1",
				Options: Options{ModelsDev: true, ModelsDevProvider: "gateway"},
			},
		},
		NewCatalog(roster.Client()),
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	out, err := modeltools.NewListModels(registry, nil).
		Run(context.Background(), `{"capability":"decision"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"provider_model": "vendor/new-model"`) ||
		!strings.Contains(out, `"decision": "declared"`) ||
		!strings.Contains(out, `"context": 65536`) ||
		strings.Contains(out, "not-in-live-roster") ||
		strings.Contains(out, `"provider_model": "chat"`) {
		t.Fatalf("decision roster = %s", out)
	}
}

func TestCatalogDiscoversNativeDecisionCapabilityWithoutModelNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"a-new-model:7b"}]}`)
		case "/api/show":
			_, _ = io.WriteString(w, `{"capabilities":["decision","vision"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	registry := modelcatalog.New(
		[]Provider{{Name: "local", Type: "ollama", BaseURL: server.URL}},
		NewCatalog(server.Client()),
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	out, err := modeltools.NewListModels(registry, nil).
		Run(context.Background(), `{"capability":"decision"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"provider_model": "a-new-model:7b"`) ||
		!strings.Contains(out, `"decision": "derived"`) {
		t.Fatalf("native decision roster = %s", out)
	}
}

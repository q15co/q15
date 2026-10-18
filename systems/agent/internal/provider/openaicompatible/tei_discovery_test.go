package openaicompatible

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

func TestOpenAIClientDiscoversTEIEmbeddingModel(t *testing.T) {
	for _, tc := range []struct {
		name, root, served, want string
	}{
		{"served alias", "", "qwen3-embedding-0.6b", "qwen3-embedding-0.6b"},
		{"artifact name", "", "", "Qwen/Qwen3-Embedding-0.6B"},
		{"API root path", "/tei", "qwen3-embedding-0.6b", "qwen3-embedding-0.6b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					paths = append(paths, r.URL.Path)
					if r.Header.Get("Authorization") != "Bearer secret-key" {
						t.Error("discovery request lost provider authentication")
					}
					if r.URL.Path == tc.root+"/info" {
						_, _ = w.Write(
							[]byte(
								`{"model_id":"Qwen/Qwen3-Embedding-0.6B","served_model_name":"` + tc.served + `","model_type":{"embedding":{"pooling":"last_token"}},"max_input_length":4096}`,
							),
						)
						return
					}
					http.NotFound(w, r)
				}),
			)
			defer server.Close()
			models, err := NewRosterClient(
				server.Client(),
			).Discover(context.Background(), modelcatalog.Provider{
				BaseURL: server.URL + tc.root + "/", APIKey: "secret-key",
			})
			if err != nil || len(models) != 1 {
				t.Fatalf("TEI models = %+v, %v", models, err)
			}
			model := models[0]
			if model.ProviderModel != tc.want || model.MaxContextTokens != 4096 ||
				!model.Capabilities.Known || !model.Capabilities.Embed || !model.Capabilities.Text ||
				model.Capabilities.Chat || model.Capabilities.Decision ||
				model.Capabilities.Provenance["embed"] != "derived" ||
				model.Capabilities.Provenance["chat"] != "derived" {
				t.Fatalf("TEI model = %+v", model)
			}
			if strings.Join(paths, ",") != tc.root+"/models,"+tc.root+"/info" {
				t.Fatalf("discovery paths = %v", paths)
			}
		})
	}
}

func TestOpenAIClientTEIDiscoveryRejectsInvalidInfo(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing endpoint", "private response", http.StatusNotFound},
		{"unauthorized", "private response", http.StatusUnauthorized},
		{"server error", "private response", http.StatusInternalServerError},
		{"malformed JSON", `{"private response":`, http.StatusOK},
		{"unknown shape", `{"private response":true}`, http.StatusOK},
		{"classifier", `{"model_id":"private response","model_type":{"classifier":{}}}`, http.StatusOK},
		{"missing artifact", `{"served_model_name":"private response","model_type":{"embedding":{}}}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/info" {
						http.NotFound(w, r)
						return
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}),
			)
			defer server.Close()
			models, err := NewRosterClient(
				server.Client(),
			).Discover(context.Background(), modelcatalog.Provider{BaseURL: server.URL})
			if err == nil || len(models) != 0 || !strings.Contains(err.Error(), "/info") ||
				strings.Contains(err.Error(), "private response") {
				t.Fatalf("invalid TEI info = %+v, %v", models, err)
			}
		})
	}
}

func TestOpenAIClientProbesTEIOnlyWhenModelsMissing(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			infoCalls := 0
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/info" {
						infoCalls++
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"data":[]}`))
				}),
			)
			defer server.Close()
			_, err := NewRosterClient(
				server.Client(),
			).Discover(context.Background(), modelcatalog.Provider{BaseURL: server.URL})
			if (err == nil) != (status == http.StatusOK) || infoCalls != 0 {
				t.Fatalf("models status %d: error = %v, info calls = %d", status, err, infoCalls)
			}
		})
	}
}

func TestOpenAIClientTEIDiscoveryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info" {
			cancel()
			<-r.Context().Done()
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	_, err := NewRosterClient(
		server.Client(),
	).Discover(ctx, modelcatalog.Provider{BaseURL: server.URL})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled TEI discovery = %v", err)
	}
}

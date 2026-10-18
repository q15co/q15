package systemone

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

func TestSystemOneRosterSupportsNativeAndCompatibleShapes(t *testing.T) {
	for _, body := range []string{
		`{"models":[{"name":"new-decision","description":"Typed decisions","release_date":"2026-10-18"}]}`,
		`{"data":[{"id":"new-decision"}]}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/custom/api/models" ||
				r.Header.Get("Authorization") != "Bearer roster-key" {
				t.Errorf("request = %s, headers %v", r.URL.Path, r.Header)
			}
			_, _ = io.WriteString(w, body)
		}))
		models, err := NewRosterClient(
			server.Client(),
		).Discover(context.Background(), modelcatalog.Provider{BaseURL: server.URL + "/custom/api", APIKey: "roster-key"})
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 1 || models[0].ProviderModel != "new-decision" ||
			!models[0].IsDecision() {
			t.Fatalf("models = %+v", models)
		}
	}
}

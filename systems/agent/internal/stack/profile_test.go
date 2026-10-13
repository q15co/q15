package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

// testRelease is a release identifier with the shape a published tag has, so
// nothing under test has to pretend a development build is a release.
const testRelease = "1.2.3"

// testProfile is the stack the rendering tests render.
func testProfile() Profile {
	return DefaultProfile(testRelease, "/home/agent/.local/state/q15")
}

// TestDefaultProfileLoadsThroughTheAgentLoader is the point of folding the CLI
// into the agent module: the configuration it renders is read back by the
// agent's own loader, so drift between the two is impossible by construction
// rather than caught by comparing a golden file.
func TestDefaultProfileLoadsThroughTheAgentLoader(t *testing.T) {
	rendered, err := testProfile().AgentConfigYAML()
	if err != nil {
		t.Fatalf("AgentConfigYAML() error = %v", err)
	}
	if !strings.Contains(string(rendered), "schema_version: 1") {
		t.Fatalf("rendered config = %q, want the schema version written", rendered)
	}
	// Credentials stay names, never values: rendering writes no secret.
	if !strings.Contains(string(rendered), "base_url_env: Q15_EMBEDDINGS_BASE_URL") {
		t.Fatalf("rendered config = %q, want the embeddings endpoint by name", rendered)
	}
	if strings.Contains(string(rendered), "api_key:") {
		t.Fatalf("rendered config = %q, want no credential value", rendered)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, rendered, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if loaded.SchemaVersion != config.SchemaVersion {
		t.Fatalf("loaded schema version = %d, want %d", loaded.SchemaVersion, config.SchemaVersion)
	}
	if loaded.Agent == nil {
		t.Fatal("loaded agent = nil, want the rendered agent")
	}
	if loaded.Agent.Name != DefaultAgentName {
		t.Fatalf("loaded agent name = %q, want %q", loaded.Agent.Name, DefaultAgentName)
	}

	provider, ok := loaded.FindProvider("ollama")
	if !ok {
		t.Fatal("loaded providers do not contain ollama")
	}
	if provider.Type != providertypes.Ollama {
		t.Fatalf("provider type = %q, want %q", provider.Type, providertypes.Ollama)
	}
	// The endpoint is the stack's own container: this is the profile a cluster
	// of units runs, not one for an ollama on the host.
	if want := testProfile().OllamaBaseURL(); provider.BaseURL != want {
		t.Fatalf("provider base url = %q, want %q", provider.BaseURL, want)
	}
	if provider.KeyEnv != "" {
		t.Fatalf("provider key env = %q, want none for a local ollama", provider.KeyEnv)
	}

	embeddings := loaded.Agent.Tools.Embeddings
	if embeddings.Provider != embed.ProviderOpenAI {
		t.Fatalf("embeddings provider = %q, want %q", embeddings.Provider, embed.ProviderOpenAI)
	}
	if embeddings.Model != DefaultEmbeddingModel {
		t.Fatalf("embeddings model = %q, want %q", embeddings.Model, DefaultEmbeddingModel)
	}
	if embeddings.Dimensions != DefaultEmbeddingDimensions {
		t.Fatalf(
			"embeddings dimensions = %d, want %d",
			embeddings.Dimensions,
			DefaultEmbeddingDimensions,
		)
	}
	if embeddings.BaseURLEnv != embeddingsURLEnv {
		t.Fatalf("embeddings base url env = %q, want %q", embeddings.BaseURLEnv, embeddingsURLEnv)
	}
	if loaded.Agent.Bridge.ListenTarget != config.DefaultBridgeListenTarget {
		t.Fatalf(
			"bridge listen target = %q, want %q",
			loaded.Agent.Bridge.ListenTarget,
			config.DefaultBridgeListenTarget,
		)
	}
}

// TestProfileRejectsIncompleteInput covers the fields a rendered stack cannot
// guess: an empty one is a mistake at the call site, not something to repair.
func TestProfileRejectsIncompleteInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Profile)
	}{
		{name: "agent name", mutate: func(p *Profile) { p.AgentName = "  " }},
		{name: "release", mutate: func(p *Profile) { p.Release = "" }},
		{name: "unreleased build", mutate: func(p *Profile) { p.Release = "dev" }},
		{name: "floating release", mutate: func(p *Profile) { p.Release = "latest" }},
		{name: "release with whitespace", mutate: func(p *Profile) { p.Release = "1.2.3 extra" }},
		{name: "state directory", mutate: func(p *Profile) { p.StateDir = " " }},
		{name: "host port", mutate: func(p *Profile) { p.HostPort = 0 }},
		{name: "host port above the range", mutate: func(p *Profile) { p.HostPort = 70000 }},
		{name: "chat model", mutate: func(p *Profile) { p.ChatModel = "" }},
		{name: "embedding model", mutate: func(p *Profile) { p.EmbeddingModel = " " }},
		{name: "embedding dimensions", mutate: func(p *Profile) { p.EmbeddingDimensions = 0 }},
		{name: "embedding batch size", mutate: func(p *Profile) { p.EmbeddingBatchSize = 0 }},
		{name: "vector store image", mutate: func(p *Profile) { p.VectorStoreImage = "" }},
		{
			name:   "vector store without a tag",
			mutate: func(p *Profile) { p.VectorStoreImage = "qdrant/qdrant" },
		},
		{
			name:   "vector store on latest",
			mutate: func(p *Profile) { p.VectorStoreImage = "qdrant/qdrant:latest" },
		},
		{name: "ollama image", mutate: func(p *Profile) { p.OllamaImage = "ollama/ollama" }},
		{name: "bridge listen target", mutate: func(p *Profile) { p.BridgeListenTarget = "" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := testProfile()
			test.mutate(&profile)

			if err := profile.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want a rejection")
			}
			if _, err := profile.Units(); err == nil {
				t.Fatal("Units() error = nil, want a rejection")
			}
			if _, err := profile.Render(t.TempDir()); err == nil {
				t.Fatal("Render() error = nil, want a rejection")
			}
		})
	}
}

// TestResourceNameIsASlug pins the derivation every resource name hangs off,
// because it is the one decision an existing deployment depends on: the default
// has to come out as exactly the names the Compose files use.
func TestResourceNameIsASlug(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		want  string
	}{
		{name: "the default", agent: "Q15", want: "q15"},
		{name: "words", agent: "Q15 Dev Stack", want: "q15-dev-stack"},
		{name: "punctuation collapses", agent: "  --my__q15!!  ", want: "my-q15"},
		{name: "digits", agent: "q15 2", want: "q15-2"},
		{name: "nothing usable", agent: "!!", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := testProfile()
			profile.AgentName = test.agent

			if got := profile.ResourceName(); got != test.want {
				t.Fatalf("ResourceName() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestResourceNameIsBounded keeps a name that is fine as a display name from
// becoming a unit, volume and network name nobody can read at once.
func TestResourceNameIsBounded(t *testing.T) {
	profile := testProfile()
	profile.AgentName = strings.Repeat("a", maxResourceName+1)
	if err := profile.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want a name that is too long refused")
	}

	profile.AgentName = strings.Repeat("a", maxResourceName)
	if err := profile.Validate(); err != nil {
		t.Fatalf("Validate() error = %v at the limit, want it accepted", err)
	}
}

// TestEndpointsFollowTheStackName is the drift this design avoids by deriving
// instead of storing: renaming a stack moves the endpoints the units set and the
// configuration names, together.
func TestEndpointsFollowTheStackName(t *testing.T) {
	profile := testProfile()
	profile.AgentName = "Demo"

	if got, want := profile.OllamaBaseURL(), "http://demo-ollama:11434"; got != want {
		t.Fatalf("OllamaBaseURL() = %q, want %q", got, want)
	}
	if got, want := profile.EmbeddingsBaseURL(), "http://demo-ollama:11434/v1"; got != want {
		t.Fatalf("EmbeddingsBaseURL() = %q, want %q", got, want)
	}
	if got, want := profile.QdrantURL(), "http://demo-qdrant:6334"; got != want {
		t.Fatalf("QdrantURL() = %q, want %q", got, want)
	}
	if got, want := profile.NetworkName(), "demo-net"; got != want {
		t.Fatalf("NetworkName() = %q, want %q", got, want)
	}
}

// TestDefaultPathsFollowTheEnvironment pins where rendered state and units go:
// the user's own directories, never a system one.
func TestDefaultPathsFollowTheEnvironment(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")

	stateDir, err := DefaultStateDir()
	if err != nil {
		t.Fatalf("DefaultStateDir() error = %v", err)
	}
	if stateDir != "/xdg/state/q15" {
		t.Fatalf("DefaultStateDir() = %q, want %q", stateDir, "/xdg/state/q15")
	}

	unitsDir, err := DefaultUnitsDir()
	if err != nil {
		t.Fatalf("DefaultUnitsDir() error = %v", err)
	}
	if unitsDir != "/xdg/config/containers/systemd" {
		t.Fatalf("DefaultUnitsDir() = %q, want %q", unitsDir, "/xdg/config/containers/systemd")
	}

	// Without the XDG variables the conventional locations are used.
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/tester")

	stateDir, err = DefaultStateDir()
	if err != nil {
		t.Fatalf("DefaultStateDir() error = %v", err)
	}
	if stateDir != "/home/tester/.local/state/q15" {
		t.Fatalf("DefaultStateDir() = %q, want the state home", stateDir)
	}

	unitsDir, err = DefaultUnitsDir()
	if err != nil {
		t.Fatalf("DefaultUnitsDir() error = %v", err)
	}
	if unitsDir != "/home/tester/.config/containers/systemd" {
		t.Fatalf("DefaultUnitsDir() = %q, want the rootless Quadlet path", unitsDir)
	}
}

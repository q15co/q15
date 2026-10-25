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
	if want := testProfile().ChatBaseURL(); provider.BaseURL != want {
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
		{name: "chat model", mutate: func(p *Profile) { p.Chat.Model = "" }},
		{name: "chat source kind", mutate: func(p *Profile) { p.Chat.Kind = "something-else" }},
		{
			name:   "external chat source without an endpoint",
			mutate: func(p *Profile) { p.Chat.Kind = SourceOllamaExternal },
		},
		{
			name: "hosted chat source without a credential",
			mutate: func(p *Profile) {
				p.Chat.Kind = SourceOpenAICompatible
				p.Chat.BaseURL = "https://chat.example.net/v1"
			},
		},
		{
			name:   "endpoint on a local chat source",
			mutate: func(p *Profile) { p.Chat.BaseURL = "http://localhost:11434" },
		},
		{
			name:   "credential on a local chat source",
			mutate: func(p *Profile) { p.Chat.KeyEnv = "Q15_CHAT_API_KEY" },
		},
		{name: "embedding model", mutate: func(p *Profile) { p.Embeddings.Model = " " }},
		{name: "embedding dimensions", mutate: func(p *Profile) { p.Embeddings.Dimensions = 0 }},
		{name: "embedding batch size", mutate: func(p *Profile) { p.Embeddings.BatchSize = 0 }},
		{
			name:   "external embedding source without an endpoint",
			mutate: func(p *Profile) { p.Embeddings.Kind = SourceOpenAICompatible },
		},
		{
			name:   "endpoint on a local embedding source",
			mutate: func(p *Profile) { p.Embeddings.BaseURL = "http://localhost:11434/v1" },
		},
		{name: "vector store kind", mutate: func(p *Profile) { p.Vectors.Kind = "chroma" }},
		{
			name:   "external vector store without an endpoint",
			mutate: func(p *Profile) { p.Vectors.Kind = VectorStoreQdrantExternal },
		},
		{
			name:   "endpoint on a local vector store",
			mutate: func(p *Profile) { p.Vectors.URL = "http://localhost:6334" },
		},
		{
			name:   "credential on a local vector store",
			mutate: func(p *Profile) { p.Vectors.KeyEnv = "QDRANT_API_KEY" },
		},
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

	if got, want := profile.ChatBaseURL(), "http://demo-ollama:11434"; got != want {
		t.Fatalf("ChatBaseURL() = %q, want %q", got, want)
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

// hostedProfile is a stack whose models and vectors are all served somewhere
// else: the shape a profile could not describe while the renderer always emitted
// both containers. Every endpoint and every credential is named, so the two
// halves of the render have something to disagree about if they ever drift.
func hostedProfile() Profile {
	profile := testProfile()
	profile.Chat = ChatSource{
		Kind:    SourceOpenAICompatible,
		Model:   "gpt-oss-120b",
		BaseURL: "https://chat.example.net/v1",
		KeyEnv:  "Q15_CHAT_API_KEY",
	}
	profile.Embeddings = EmbeddingSource{
		Kind:       SourceOpenAICompatible,
		Model:      "text-embedding-3-small",
		Dimensions: 1536,
		BatchSize:  64,
		BaseURL:    "https://embeddings.example.net/v1",
		KeyEnv:     "Q15_EMBED_API_KEY",
	}
	profile.Vectors = VectorStore{
		Kind:   VectorStoreQdrantExternal,
		URL:    "https://vectors.example.net:6334",
		KeyEnv: "QDRANT_API_KEY",
	}
	return profile
}

// mixedProfile reaches chat and the vector store elsewhere while embeddings keep
// the model server this stack runs: the container set follows the sources one by
// one, not the profile as a whole.
func mixedProfile() Profile {
	profile := hostedProfile()
	profile.Embeddings = EmbeddingSource{
		Kind:       SourceOllamaLocal,
		Model:      DefaultEmbeddingModel,
		Dimensions: DefaultEmbeddingDimensions,
		BatchSize:  DefaultEmbeddingBatchSize,
	}
	return profile
}

// TestDefaultProfileIsLocalAndKeyless is the shape every existing deployment
// has: the stack runs both servers, and no credential is prepared for either,
// because a local server takes none.
func TestDefaultProfileIsLocalAndKeyless(t *testing.T) {
	profile := testProfile()

	if got, want := profile.Chat.Kind, SourceOllamaLocal; got != want {
		t.Fatalf("chat kind = %q, want %q", got, want)
	}
	if got, want := profile.Embeddings.Kind, SourceOllamaLocal; got != want {
		t.Fatalf("embeddings kind = %q, want %q", got, want)
	}
	if got, want := profile.Vectors.Kind, VectorStoreQdrantLocal; got != want {
		t.Fatalf("vector store kind = %q, want %q", got, want)
	}
	if got, want := profile.Chat.Model, DefaultChatModel; got != want {
		t.Fatalf("chat model = %q, want %q", got, want)
	}
	if got, want := profile.Embeddings.Model, DefaultEmbeddingModel; got != want {
		t.Fatalf("embedding model = %q, want %q", got, want)
	}

	for _, declared := range []struct {
		what string
		key  string
	}{
		{what: "chat", key: profile.Chat.KeyEnv},
		{what: "embeddings", key: profile.Embeddings.KeyEnv},
		{what: "the vector store", key: profile.Vectors.KeyEnv},
	} {
		if declared.key != "" {
			t.Fatalf("%s declares key env %q for a local source", declared.what, declared.key)
		}
	}

	// A profile that runs both servers locally still needs their pins.
	if err := profile.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestHostedProfileRendersNoModelContainers is the container-set criterion: a
// stack that runs nothing itself renders the four q15 images and the network,
// and nothing in the render may name a container that is not there.
func TestHostedProfileRendersNoModelContainers(t *testing.T) {
	profile := hostedProfile()
	units, err := profile.Units()
	if err != nil {
		t.Fatalf("Units() error = %v", err)
	}

	want := []string{
		"q15-agent.container",
		"q15-exec.container",
		"q15-net.network",
		"q15-postgres.container",
		"q15-proxy.container",
		"q15-web.container",
	}
	names := make([]string, 0, len(units))
	for _, unit := range units {
		names = append(names, unit.Name)
		for _, line := range strings.Split(string(unit.Contents), "\n") {
			switch {
			case strings.Contains(line, "ollama.container"),
				strings.Contains(line, "qdrant.container"):
				t.Fatalf("%s names a container the profile does not run: %s", unit.Name, line)
			case strings.Contains(line, "q15_ollama_models"),
				strings.Contains(line, "q15_qdrant_storage"):
				t.Fatalf(
					"%s mounts data for a container the profile does not run: %s",
					unit.Name,
					line,
				)
			}
		}
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("rendered units = %v, want %v", names, want)
	}
	if got := profile.UnitNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("UnitNames() = %v, want the units Units() renders", got)
	}
}

// TestMixedProfileRunsTheServersItUses pins the per-source decision: two local
// kinds share one container, and one local source is enough to render it.
func TestMixedProfileRunsTheServersItUses(t *testing.T) {
	units := rendered(t, mixedProfile())

	if _, ok := units["q15-ollama.container"]; !ok {
		t.Fatal("the model server is not rendered, but the embeddings are configured to use it")
	}
	if _, ok := units["q15-qdrant.container"]; ok {
		t.Fatal("the vector store is rendered, but the profile keeps its vectors elsewhere")
	}

	agent := units["q15-agent.container"]
	if got, want := environment(t, agent, embeddingsURLEnv), "http://q15-ollama:11434/v1"; got != want {
		t.Fatalf("%s = %q, want %q", embeddingsURLEnv, got, want)
	}
	if got, want := environment(t, agent, qdrantURLEnv), "https://vectors.example.net:6334"; got != want {
		t.Fatalf("%s = %q, want %q", qdrantURLEnv, got, want)
	}
}

// TestEndpointsFollowTheSourceKinds pins the derivation in the mixed case: an
// ollama elsewhere and embeddings still here are two endpoints, one named and one
// derived from the stack's name, and the agent reads whichever applies.
func TestEndpointsFollowTheSourceKinds(t *testing.T) {
	profile := testProfile()
	profile.Chat = ChatSource{
		Kind:    SourceOllamaExternal,
		Model:   DefaultChatModel,
		BaseURL: "http://192.168.1.10:11434/",
	}
	if err := profile.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got, want := profile.ChatBaseURL(), "http://192.168.1.10:11434"; got != want {
		t.Fatalf("ChatBaseURL() = %q, want %q", got, want)
	}
	if got, want := profile.EmbeddingsBaseURL(), "http://q15-ollama:11434/v1"; got != want {
		t.Fatalf("EmbeddingsBaseURL() = %q, want %q", got, want)
	}
	if got, want := profile.QdrantURL(), "http://q15-qdrant:6334"; got != want {
		t.Fatalf("QdrantURL() = %q, want %q", got, want)
	}

	units := rendered(t, profile)
	if _, ok := units["q15-ollama.container"]; !ok {
		t.Fatal("the model server is not rendered, but the embeddings are configured to use it")
	}
	chatProvider := units["q15-agent.container"]
	if got, want := environment(t, chatProvider, embeddingsURLEnv), "http://q15-ollama:11434/v1"; got != want {
		t.Fatalf("%s = %q, want %q", embeddingsURLEnv, got, want)
	}
}

// TestAnOllamaElsewhereServesBothEndpoints is the case #221 probes for: a model
// server already running somewhere else, used for chat and for embeddings.
// Nothing runs here, and the embeddings path is ollama's OpenAI shape rather
// than the root the profile named.
func TestAnOllamaElsewhereServesBothEndpoints(t *testing.T) {
	profile := testProfile()
	profile.Chat = ChatSource{
		Kind:    SourceOllamaExternal,
		Model:   DefaultChatModel,
		BaseURL: "http://192.168.1.10:11434",
	}
	profile.Embeddings = EmbeddingSource{
		Kind:       SourceOllamaExternal,
		Model:      DefaultEmbeddingModel,
		Dimensions: DefaultEmbeddingDimensions,
		BatchSize:  DefaultEmbeddingBatchSize,
		BaseURL:    "http://192.168.1.10:11434",
	}

	if err := profile.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got, want := profile.ChatBaseURL(), "http://192.168.1.10:11434"; got != want {
		t.Fatalf("ChatBaseURL() = %q, want %q", got, want)
	}
	if got, want := profile.EmbeddingsBaseURL(), "http://192.168.1.10:11434/v1"; got != want {
		t.Fatalf("EmbeddingsBaseURL() = %q, want %q", got, want)
	}

	units := rendered(t, profile)
	if _, ok := units["q15-ollama.container"]; ok {
		t.Fatal("the model server is rendered, but both sources name one somewhere else")
	}
	// The vector store is this stack's own: moving the model server out did not
	// move anything else.
	if _, ok := units["q15-qdrant.container"]; !ok {
		t.Fatal("the vector store is not rendered, but this profile still runs it")
	}
}

// TestHostedProfileCarriesItsEndpointsAndCredentials is the configuration half:
// the provider entry, the two endpoints and the key environment variables the
// sources name, in a file the agent's own loader accepts.
func TestHostedProfileCarriesItsEndpointsAndCredentials(t *testing.T) {
	profile := hostedProfile()
	cfg, err := profile.AgentConfig()
	if err != nil {
		t.Fatalf("AgentConfig() error = %v", err)
	}

	provider, ok := cfg.FindProvider("openai-compatible")
	if !ok {
		t.Fatalf("providers = %#v, want one for the hosted chat endpoint", cfg.Providers)
	}
	if got, want := provider.Type, providertypes.OpenAICompatible; got != want {
		t.Fatalf("provider type = %q, want %q", got, want)
	}
	if got, want := provider.BaseURL, "https://chat.example.net/v1"; got != want {
		t.Fatalf("provider base url = %q, want %q", got, want)
	}
	if got, want := provider.KeyEnv, "Q15_CHAT_API_KEY"; got != want {
		t.Fatalf("provider key env = %q, want %q", got, want)
	}

	embeddings := cfg.Agent.Tools.Embeddings
	if got, want := embeddings.APIKeyEnv, "Q15_EMBED_API_KEY"; got != want {
		t.Fatalf("embeddings api key env = %q, want %q", got, want)
	}
	if got, want := embeddings.QdrantAPIKeyEnv, "QDRANT_API_KEY"; got != want {
		t.Fatalf("embeddings vector store key env = %q, want %q", got, want)
	}
	if got, want := embeddings.BaseURLEnv, embeddingsURLEnv; got != want {
		t.Fatalf("embeddings base url env = %q, want %q", got, want)
	}
	if got, want := embeddings.QdrantURLEnv, qdrantURLEnv; got != want {
		t.Fatalf("embeddings vector store url env = %q, want %q", got, want)
	}
	if got, want := embeddings.Dimensions, 1536; got != want {
		t.Fatalf("embeddings dimensions = %d, want %d", got, want)
	}

	// Credentials stay names: the rendered file carries the variable, never a
	// value, so nothing written to disk is a secret.
	rendered, err := profile.AgentConfigYAML()
	if err != nil {
		t.Fatalf("AgentConfigYAML() error = %v", err)
	}
	if !strings.Contains(string(rendered), "key_env: Q15_CHAT_API_KEY") {
		t.Fatalf("rendered config = %q, want the provider credential by name", rendered)
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
	if _, ok := loaded.FindProvider("openai-compatible"); !ok {
		t.Fatalf(
			"loaded providers = %#v, want the hosted one to survive the round trip",
			loaded.Providers,
		)
	}
}

// TestAModelServerImageIsOnlyRequiredWhereItRuns keeps an unused pin from being
// demanded: a profile that runs neither server names neither image.
func TestAModelServerImageIsOnlyRequiredWhereItRuns(t *testing.T) {
	profile := hostedProfile()
	profile.VectorStoreImage = ""
	profile.OllamaImage = ""
	if err := profile.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want a profile that runs no server to need no image", err)
	}
	if _, err := profile.Units(); err != nil {
		t.Fatalf("Units() error = %v", err)
	}
}

// Package stack defines a q15 stack as data and renders it as systemd Quadlet
// units.
//
// A stack is a deployment, not a process. The profile names the containers, the
// images and the models they run, the network and the volumes they own, and the
// port a person reaches them on. The units are rendered from that profile, and
// so are the two files a stack consumes, the agent's configuration and the
// proxy's policy, so the models a stack pulls and the models it is configured to
// use cannot drift apart. That last import is why the command line lives inside
// the agent module rather than in one of its own: it builds the agent's real
// configuration type, and the proxy's policy comes from the contract package
// both sides share, because Go's internal/ rule would otherwise put the
// proxy's own config out of this module's reach.
package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
	"go.yaml.in/yaml/v3"
)

const (
	// DefaultAgentName is the display name a stack is created with. Every unit,
	// network and volume name is derived from it, so the default is also what
	// makes a first install adopt the data an existing Compose deployment
	// already holds: "Q15" renders as q15, and q15_workspace is the volume the
	// Compose files name.
	DefaultAgentName = "Q15"

	// releaseImageRepository hosts the four q15 images. They share one tag: the
	// release the CLI was built from.
	releaseImageRepository = "ghcr.io/q15co"

	// VectorStoreImage is the vector store's image, pinned to an explicit
	// version. It is a compatibility axis of its own: the q15 images move with
	// every release, while the vector store moves only when its API or the
	// collection format does, and a stack whose collection already exists must
	// not be dragged along by a release that has nothing to do with it.
	//
	// Fully qualified, like every other image here: a short name is resolved
	// through the host's unqualified-search-registries and short-name-aliases,
	// which are empty on a stock NixOS install and restricted elsewhere, so a
	// short name is a stack that cannot pull.
	VectorStoreImage = "docker.io/qdrant/qdrant:v1.17.1"

	// OllamaImage is the model server's image, pinned and qualified for the same
	// reasons.
	OllamaImage = "docker.io/ollama/ollama:0.40.7"

	// PostgresImage supplies the conversation store and pgvector.
	PostgresImage = "docker.io/pgvector/pgvector:0.8.7-pg17"

	// DefaultChatModel is the model a stack pulls for chat: small enough to run
	// without a GPU, because the install profile is the one somebody evaluates
	// on a laptop.
	DefaultChatModel = "qwen3:8b"

	// DefaultEmbeddingModel is the model a stack pulls for embeddings.
	DefaultEmbeddingModel = "qwen3-embedding-0.6b"

	// DefaultEmbeddingDimensions is the dimension the vector collection is
	// created at. It and the model are one decision: the agent refuses a mismatch
	// rather than degrading silently.
	DefaultEmbeddingDimensions = 1024

	// DefaultEmbeddingBatchSize is the batch size the agent asks the embedding
	// server for.
	DefaultEmbeddingBatchSize = 128

	// DefaultHostPort is the loopback port the browser client publishes. Only
	// the browser client binds a port at all; everything else is reachable only
	// from inside the stack, and this one is bound to loopback rather than to
	// every interface.
	DefaultHostPort = 8080

	// The environment variable names the agent reads the stack's endpoints
	// from. They are constants rather than fields because the units set them and
	// the agent's configuration names them: one spelling, or the two drift.
	embeddingsURLEnv = "Q15_EMBEDDINGS_BASE_URL"
	qdrantURLEnv     = "Q15_QDRANT_URL"

	// The paths inside the containers, and the one credential file the shipped
	// proxy policy names. Podman delivers each declared secret under /run/secrets.
	//
	// Only the proxy's token appears here, because the stock policy declares that
	// secret and the proxy refuses to start without it. A provider key is a
	// different matter: it belongs in the units when the profile declares a
	// provider that needs one, and the default profile declares a local ollama,
	// which needs none. Preparing a credential nothing reads is how an operator
	// ends up asking why their stack is waiting for somebody else's key.
	secretsDirTarget  = "/run/secrets"
	agentConfigTarget = "/etc/q15/agent/config.yaml"
	authDirTarget     = "/etc/q15/auth"
	proxyPolicyTarget = "/etc/q15/proxy/policy.yaml"
	githubTokenFile   = "github_token"

	// maxResourceName bounds the name derived from the agent's name. Systemd
	// unit names are not the constraint; a name that stays readable as a unit, a
	// container, a volume and a network at once is.
	maxResourceName = 40

	// unReleasedBuild is the release identifier a build made outside a release
	// carries. It names no published image, so no stack can be rendered from it.
	unReleasedBuild = "dev"
)

// The kinds of model source a profile can name. A stack runs the server itself
// when the kind is local and reaches one it does not run when the kind is
// external, which is the whole of what the container set is derived from.
const (
	// SourceOllamaLocal is an ollama this stack runs.
	SourceOllamaLocal = "ollama-local"
	// SourceOllamaExternal is an ollama somebody else runs, reached at the
	// endpoint the profile names.
	SourceOllamaExternal = "ollama-external"
	// SourceOpenAICompatible is any hosted OpenAI-compatible endpoint.
	SourceOpenAICompatible = "openai-compatible"

	// VectorStoreQdrantLocal is a Qdrant this stack runs.
	VectorStoreQdrantLocal = "qdrant-local"
	// VectorStoreQdrantExternal is a Qdrant somebody else runs, which
	// authenticates with a key.
	VectorStoreQdrantExternal = "qdrant-external"
)

// ChatSource names the endpoint the agent chats through.
type ChatSource struct {
	// Kind is one of the source kinds above.
	Kind string
	// Model is the chat model this endpoint serves.
	Model string
	// BaseURL is the endpoint when the kind is external. It is empty for a local
	// kind, whose endpoint is derived from the stack's name, and refused beside
	// one: an endpoint named next to a container that runs here is two answers
	// to one question.
	BaseURL string
	// KeyEnv is the environment variable the agent reads this endpoint's
	// credential from. Empty when the endpoint needs none.
	KeyEnv string
}

// EmbeddingSource names the endpoint the agent embeds through. It has the shape
// of a chat source plus the vector width and the batch size, which belong to the
// embedding model rather than to the endpoint.
type EmbeddingSource struct {
	Kind       string
	Model      string
	BaseURL    string
	KeyEnv     string
	Dimensions int
	BatchSize  int
}

// VectorStore names where the vectors live.
type VectorStore struct {
	// Kind is one of the vector store kinds above.
	Kind string
	// URL is the endpoint when the kind is external, on the same terms as a
	// model source's BaseURL.
	URL string
	// KeyEnv is the environment variable holding the store's credential. Empty
	// for a local store, which takes none.
	KeyEnv string
}

// Profile is the deployment-shaped input a stack is created from. It carries no
// secrets: every credential stays the name of a file, so rendering never writes
// one to disk.
type Profile struct {
	// AgentName is the agent's display name. The resource name every unit,
	// network and volume carries is derived from it, so renaming the agent later
	// is a migration rather than an edit.
	AgentName string
	// Release is the identifier of the q15 release the CLI was built from. It is
	// the tag of all four q15 images, written literally into the units: a
	// floating tag must never reach a rendered unit, because a tag advancing
	// under a running stack is exactly the torn release the one-version rule
	// exists to prevent.
	Release string
	// StateDir is the host directory holding what the CLI rendered: the agent's
	// configuration, the auth store and the secrets the containers read.
	StateDir string
	// HostPort is the loopback port the browser client publishes.
	HostPort int
	// Chat, Embeddings and Vectors name where the models and the vectors are
	// served, each one local or external. The container set is derived from these
	// three values, and the agent's configuration is rendered from the same ones,
	// so the endpoint a stack pulls a model on and the endpoint it is configured
	// to use cannot disagree.
	Chat       ChatSource
	Embeddings EmbeddingSource
	Vectors    VectorStore
	// VectorStoreImage and OllamaImage are pinned by version, and are required
	// only for the servers this profile runs itself.
	VectorStoreImage string
	OllamaImage      string
	// BridgeListenTarget is the unix socket the chat bridge listens on.
	BridgeListenTarget string
}

// DefaultProfile is the profile a new stack renders: the ollama-first CPU
// profile, one model server for chat and embeddings, one vector store, and the
// four q15 images at the CLI's own release.
func DefaultProfile(release, stateDir string) Profile {
	return Profile{
		AgentName: DefaultAgentName,
		Release:   release,
		StateDir:  stateDir,
		HostPort:  DefaultHostPort,
		Chat:      ChatSource{Kind: SourceOllamaLocal, Model: DefaultChatModel},
		Embeddings: EmbeddingSource{
			Kind:       SourceOllamaLocal,
			Model:      DefaultEmbeddingModel,
			Dimensions: DefaultEmbeddingDimensions,
			BatchSize:  DefaultEmbeddingBatchSize,
		},
		Vectors:            VectorStore{Kind: VectorStoreQdrantLocal},
		VectorStoreImage:   VectorStoreImage,
		OllamaImage:        OllamaImage,
		BridgeListenTarget: config.DefaultBridgeListenTarget,
	}
}

// ResourceName is the name every resource of this stack carries: the agent's
// display name as a lower-case slug, so "Q15" renders as q15 and a second stack
// named something else cannot collide with it. Runs of anything that is not a
// letter or a digit become one dash, and leading and trailing dashes are
// dropped, because a unit name may not start with one.
func (p Profile) ResourceName() string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(p.AgentName) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// localOllamaURL is where a model server this stack runs is reached. The install
// profile runs ollama beside the agent, so this is the container's name on the
// stack's network, which is why every container carries an explicit name.
func (p Profile) localOllamaURL() string {
	return "http://" + p.resource("ollama") + ":11434"
}

// ChatBaseURL is where the agent chats: the container this stack runs for a
// local source, and whatever the profile named for an external one. The kinds
// are exhaustive over a validated profile; an unvalidated one has no endpoint to
// report and gets an empty string.
func (p Profile) ChatBaseURL() string {
	switch p.Chat.Kind {
	case SourceOllamaLocal:
		return p.localOllamaURL()
	case SourceOllamaExternal, SourceOpenAICompatible:
		return endpoint(p.Chat.BaseURL)
	}
	return ""
}

// EmbeddingsBaseURL is the OpenAI-compatible embeddings endpoint the agent
// calls. Either ollama kind ends in /v1, because that is where ollama serves the
// OpenAI shape and the client appends only /embeddings to what it is given: a
// model server this stack runs and one somebody else runs are addressed the same
// way. A hosted OpenAI-compatible endpoint is whatever the profile named, as it
// named it.
func (p Profile) EmbeddingsBaseURL() string {
	switch p.Embeddings.Kind {
	case SourceOllamaLocal:
		return p.localOllamaURL() + "/v1"
	case SourceOllamaExternal:
		return endpoint(p.Embeddings.BaseURL) + "/v1"
	case SourceOpenAICompatible:
		return endpoint(p.Embeddings.BaseURL)
	}
	return ""
}

// QdrantURL is where the vectors live: the container this stack runs, or the
// endpoint the profile named.
func (p Profile) QdrantURL() string {
	switch p.Vectors.Kind {
	case VectorStoreQdrantLocal:
		return "http://" + p.resource("qdrant") + ":6334"
	case VectorStoreQdrantExternal:
		return endpoint(p.Vectors.URL)
	}
	return ""
}

// runsOllama reports whether this stack runs the model server. Two sources can
// share one, so either of them being local is enough.
func (p Profile) runsOllama() bool {
	return p.Chat.Kind == SourceOllamaLocal || p.Embeddings.Kind == SourceOllamaLocal
}

// runsVectorStore reports whether this stack runs the vector store. One source
// owns it, so there is nothing to combine.
func (p Profile) runsVectorStore() bool {
	return p.Vectors.Kind == VectorStoreQdrantLocal
}

// endpoint is an endpoint a profile named, without the trailing slash people
// type out of habit: the clients append a path to what they are given.
func endpoint(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// resource returns the stack's name for one service, for example q15-web.
func (p Profile) resource(service string) string {
	return p.ResourceName() + "-" + service
}

// volumeName is the podman volume the stack owns, named as the Compose files
// name it: <resource>_<suffix>, which for the default agent name is exactly the
// volume an existing deployment already holds its data in.
func (p Profile) volumeName(suffix string) string {
	return p.ResourceName() + "_" + suffix
}

// NetworkName is the name of the network every container joins.
func (p Profile) NetworkName() string {
	return p.ResourceName() + "-net"
}

// DefaultStateDir is where a stack's rendered state lives: the CLI's own
// directory under the user's state home, which is neither a config directory
// nor a mount point inside any container.
func DefaultStateDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); dir != "" {
		return filepath.Join(dir, "q15"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".local", "state", "q15"), nil
}

// DefaultUnitsDir is where the user's systemd looks for container units: the
// rootless Quadlet search path a rendering installs into.
func DefaultUnitsDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "containers", "systemd"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".config", "containers", "systemd"), nil
}

// AgentConfig renders the profile as the agent's own configuration type and
// validates it, so a configuration the CLI writes is one the agent accepts.
// Drift between the two is impossible by construction rather than caught by a
// test comparing them.
func (p Profile) AgentConfig() (config.Config, error) {
	if err := p.Validate(); err != nil {
		return config.Config{}, err
	}

	// The provider is named after what it is, because a stack has one chat
	// source and the profile does not name a provider: either ollama kind is the
	// ollama API, and only the hosted one changes type. A key is passed through
	// when the source declared one, which validation allows only for an external
	// source.
	provider := config.Provider{
		Name:    "ollama",
		Type:    providertypes.Ollama,
		BaseURL: p.ChatBaseURL(),
		KeyEnv:  p.Chat.KeyEnv,
		// A model server this stack runs has no models.dev entry to be enriched
		// with, so discovery stays offline for either ollama kind.
		Discovery: config.ProviderDiscovery{ModelsDev: false},
	}
	if p.Chat.Kind == SourceOpenAICompatible {
		provider.Name = providertypes.OpenAICompatible
		provider.Type = providertypes.OpenAICompatible
	}

	cfg := config.Config{
		SchemaVersion: config.SchemaVersion,
		Providers:     []config.Provider{provider},
		Agent: &config.Agent{
			Name: p.AgentName,
			History: config.History{
				DatabaseURLEnv:      "Q15_DATABASE_URL",
				EmbeddingProvider:   p.historyEmbeddingProvider(),
				EmbeddingBaseURL:    p.historyEmbeddingURL(),
				EmbeddingModel:      p.Embeddings.Model,
				EmbeddingDimensions: p.Embeddings.Dimensions,
				EmbeddingBatchSize:  p.Embeddings.BatchSize,
			},
			Tools: config.Tools{
				Embeddings: config.EmbeddingsTool{
					QdrantURLEnv:    qdrantURLEnv,
					QdrantAPIKeyEnv: p.Vectors.KeyEnv,
					Provider:        embed.ProviderOpenAI,
					BaseURLEnv:      embeddingsURLEnv,
					APIKeyEnv:       p.Embeddings.KeyEnv,
					Model:           p.Embeddings.Model,
					Dimensions:      p.Embeddings.Dimensions,
					BatchSize:       p.Embeddings.BatchSize,
				},
			},
			Bridge: config.Bridge{ListenTarget: p.BridgeListenTarget},
		},
	}

	if provider.Type == providertypes.Ollama {
		cfg.Agent.History.SummaryProvider = provider.Name
		cfg.Agent.History.SummaryModel = p.Chat.Model
		if provider.BaseURL == "https://ollama.com" {
			cfg.Agent.History.SummaryModel = "minimax-m3.1-flash"
		}
	} else {
		cfg.Providers = append(cfg.Providers, config.Provider{Name: "ollama-cloud", Type: providertypes.Ollama, BaseURL: "https://ollama.com", KeyEnv: "OLLAMA_API_KEY"})
		cfg.Agent.History.SummaryProvider = "ollama-cloud"
		cfg.Agent.History.SummaryModel = "minimax-m3.1-flash"
	}
	cfg.Agent.History.EmbeddingAPIKeyEnv = p.Embeddings.KeyEnv
	if err := cfg.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("stack: %w", err)
	}
	return cfg, nil
}

// AgentConfigYAML renders the validated configuration as the YAML a stack
// consumes. Validation happens here rather than at the loader, so a mistake is
// a refused render and not a stack that starts and then exits.
func (p Profile) AgentConfigYAML() ([]byte, error) {
	cfg, err := p.AgentConfig()
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(cfg)
}

// Validate reports whether the profile can be rendered. Nothing here repairs a
// field: a stack whose name, release or port is missing is a mistake at the call
// site, and rendering it would write units that run the wrong image or bind the
// wrong port.
func (p Profile) Validate() error {
	name := p.ResourceName()
	switch {
	case name == "":
		return errors.New("stack: the agent name yields an empty resource name")
	case len(name) > maxResourceName:
		return fmt.Errorf(
			"stack: resource name %q is longer than %d characters",
			name,
			maxResourceName,
		)
	case !isAlphaNumeric(name[0]):
		return fmt.Errorf("stack: resource name %q must start with a letter or a digit", name)
	}

	if err := validateTag("the release", p.Release); err != nil {
		return err
	}
	if p.Release == unReleasedBuild {
		return fmt.Errorf(
			"stack: release %q is not a release, so the four q15 images have no tag to pin; install q15 from a release",
			p.Release,
		)
	}

	switch {
	case strings.TrimSpace(p.StateDir) == "":
		return errors.New("stack: a state directory is required")
	case p.HostPort < 1 || p.HostPort > 65535:
		return fmt.Errorf("stack: host port %d is not a port", p.HostPort)
	case strings.TrimSpace(p.BridgeListenTarget) == "":
		return errors.New("stack: a bridge listen target is required")
	}

	if err := validateSource(
		"chat",
		p.Chat.Kind,
		p.Chat.Model,
		p.Chat.BaseURL,
		p.Chat.KeyEnv,
	); err != nil {
		return err
	}
	// A hosted chat endpoint has no keyless mode: the agent's own validation
	// refuses an openai-compatible provider with no key_env, so the profile says
	// so first, where the message can name the profile rather than the field.
	if p.Chat.Kind == SourceOpenAICompatible && strings.TrimSpace(p.Chat.KeyEnv) == "" {
		return errors.New(
			"stack: a hosted chat source needs the credential environment variable it authenticates with",
		)
	}
	if err := validateSource(
		"embedding",
		p.Embeddings.Kind,
		p.Embeddings.Model,
		p.Embeddings.BaseURL,
		p.Embeddings.KeyEnv,
	); err != nil {
		return err
	}
	switch {
	case p.Embeddings.Dimensions < 1:
		return fmt.Errorf(
			"stack: embedding dimensions %d must be positive",
			p.Embeddings.Dimensions,
		)
	case p.Embeddings.BatchSize < 1:
		return fmt.Errorf(
			"stack: embedding batch size %d must be positive",
			p.Embeddings.BatchSize,
		)
	}
	if err := validateVectorStore(p.Vectors); err != nil {
		return err
	}

	for _, pin := range []struct {
		what  string
		image string
		runs  bool
	}{
		{what: "vector store image", image: p.VectorStoreImage, runs: p.runsVectorStore()},
		{what: "ollama image", image: p.OllamaImage, runs: p.runsOllama()},
	} {
		// A server this profile does not run is a server whose image it does not
		// need: demanding the pin would be demanding it for nothing.
		if !pin.runs {
			continue
		}
		if err := validatePinnedImage(pin.what, pin.image); err != nil {
			return err
		}
	}
	return nil
}

// validateSource checks one model source: a kind a renderer understands, an
// endpoint for the kinds that do not run here, neither an endpoint nor a
// credential for the kind that does, and a model whichever it is.
func validateSource(what, kind, model, baseURL, keyEnv string) error {
	switch kind {
	case SourceOllamaLocal:
		if endpoint(baseURL) != "" {
			return fmt.Errorf(
				"stack: the %s source runs in this stack, so its endpoint is derived from the stack's name; %q is unreachable",
				what,
				baseURL,
			)
		}
		if strings.TrimSpace(keyEnv) != "" {
			return fmt.Errorf(
				"stack: the %s source runs in this stack and takes no credential, so %q would be prepared for nothing",
				what,
				keyEnv,
			)
		}
	case SourceOllamaExternal, SourceOpenAICompatible:
		if endpoint(baseURL) == "" {
			return fmt.Errorf(
				"stack: the %s source is %s, so it needs the endpoint it is reached at",
				what,
				kind,
			)
		}
	default:
		return fmt.Errorf("stack: %q is not a source kind for the %s", kind, what)
	}
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("stack: a model is required for the %s source", what)
	}
	return nil
}

// validateVectorStore is the same shape for the one store: the local kind is
// derived and keyless, and the external one names both.
func validateVectorStore(store VectorStore) error {
	switch store.Kind {
	case VectorStoreQdrantLocal:
		if endpoint(store.URL) != "" {
			return fmt.Errorf(
				"stack: the vector store runs in this stack, so its endpoint is derived from the stack's name; %q is unreachable",
				store.URL,
			)
		}
		if strings.TrimSpace(store.KeyEnv) != "" {
			return fmt.Errorf(
				"stack: the vector store runs in this stack and takes no credential, so %q would be prepared for nothing",
				store.KeyEnv,
			)
		}
	case VectorStoreQdrantExternal:
		if endpoint(store.URL) == "" {
			return fmt.Errorf(
				"stack: the vector store is %s, so it needs the endpoint it is reached at",
				store.Kind,
			)
		}
	default:
		return fmt.Errorf("stack: %q is not a vector store kind", store.Kind)
	}
	return nil
}

// validatePinnedImage refuses an image reference that carries no tag, or the
// floating one. A pinned version is the whole point of rendering a unit: the
// vector store and the model server are not versioned with the CLI, so nothing
// else keeps them still.
func validatePinnedImage(what, image string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return fmt.Errorf("stack: %s is required", what)
	}
	if strings.ContainsAny(image, " \t\n") {
		return fmt.Errorf("stack: %s %q contains whitespace", what, image)
	}
	_, tag, ok := strings.Cut(image, ":")
	if !ok || tag == "" {
		return fmt.Errorf("stack: %s %q carries no version tag", what, image)
	}
	if tag == "latest" {
		return fmt.Errorf("stack: %s %q is a floating tag", what, image)
	}
	return nil
}

// validateTag refuses a tag that cannot appear in a unit file, and the floating
// one, which would make every rendered unit a guess.
func validateTag(what, tag string) error {
	switch {
	case strings.TrimSpace(tag) == "":
		return fmt.Errorf("stack: %s is required", what)
	case strings.ContainsAny(tag, " \t\n"):
		return fmt.Errorf("stack: %s %q contains whitespace", what, tag)
	case tag == "latest":
		return fmt.Errorf("stack: %s %q is a floating tag", what, tag)
	}
	return nil
}

// isAlphaNumeric reports whether b is a letter or a digit, the two characters a
// resource name may start with.
func isAlphaNumeric(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

func (p Profile) historyEmbeddingProvider() string {
	if p.Embeddings.Kind == SourceOpenAICompatible {
		return "openai"
	}
	return "ollama"
}
func (p Profile) historyEmbeddingURL() string {
	if p.Embeddings.Kind == SourceOpenAICompatible {
		return p.EmbeddingsBaseURL()
	}
	return strings.TrimSuffix(p.EmbeddingsBaseURL(), "/v1")
}

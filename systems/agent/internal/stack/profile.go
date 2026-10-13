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
	OllamaImage = "docker.io/ollama/ollama:0.40.3"

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
	// ChatModel is the model the stack pulls for chat.
	ChatModel string
	// EmbeddingModel and EmbeddingDimensions are the model the stack pulls for
	// embeddings and the dimension the collection is created at.
	EmbeddingModel      string
	EmbeddingDimensions int
	// EmbeddingBatchSize is the batch size the agent asks the embedding server
	// for.
	EmbeddingBatchSize int
	// VectorStoreImage and OllamaImage are pinned by version.
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
		AgentName:           DefaultAgentName,
		Release:             release,
		StateDir:            stateDir,
		HostPort:            DefaultHostPort,
		ChatModel:           DefaultChatModel,
		EmbeddingModel:      DefaultEmbeddingModel,
		EmbeddingDimensions: DefaultEmbeddingDimensions,
		EmbeddingBatchSize:  DefaultEmbeddingBatchSize,
		VectorStoreImage:    VectorStoreImage,
		OllamaImage:         OllamaImage,
		BridgeListenTarget:  config.DefaultBridgeListenTarget,
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

// OllamaBaseURL is where the agent reaches the model server. The install
// profile runs ollama inside the stack, so this is the container's name on the
// stack's network, which is why every container carries an explicit name. A
// host's own ollama is a different endpoint and belongs to the setup that
// adopts it, not to a rename.
func (p Profile) OllamaBaseURL() string {
	return "http://" + p.resource("ollama") + ":11434"
}

// EmbeddingsBaseURL is the OpenAI-compatible embeddings endpoint the agent
// calls. It ends in /v1 because the client appends only /embeddings, which is
// where ollama serves the OpenAI shape.
func (p Profile) EmbeddingsBaseURL() string {
	return p.OllamaBaseURL() + "/v1"
}

// QdrantURL is the vector store's endpoint on the stack's network.
func (p Profile) QdrantURL() string {
	return "http://" + p.resource("qdrant") + ":6334"
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

	cfg := config.Config{
		SchemaVersion: config.SchemaVersion,
		Providers: []config.Provider{
			{
				Name:    "ollama",
				Type:    providertypes.Ollama,
				BaseURL: p.OllamaBaseURL(),
				// A locally served model has no models.dev entry to be
				// enriched with, so discovery stays offline.
				Discovery: config.ProviderDiscovery{ModelsDev: false},
			},
		},
		Agent: &config.Agent{
			Name: p.AgentName,
			Tools: config.Tools{
				Embeddings: config.EmbeddingsTool{
					QdrantURLEnv: qdrantURLEnv,
					Provider:     embed.ProviderOpenAI,
					BaseURLEnv:   embeddingsURLEnv,
					Model:        p.EmbeddingModel,
					Dimensions:   p.EmbeddingDimensions,
					BatchSize:    p.EmbeddingBatchSize,
				},
			},
			Bridge: config.Bridge{ListenTarget: p.BridgeListenTarget},
		},
	}

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
	case strings.TrimSpace(p.ChatModel) == "":
		return errors.New("stack: a chat model is required")
	case strings.TrimSpace(p.EmbeddingModel) == "":
		return errors.New("stack: an embedding model is required")
	case p.EmbeddingDimensions < 1:
		return fmt.Errorf("stack: embedding dimensions %d must be positive", p.EmbeddingDimensions)
	case p.EmbeddingBatchSize < 1:
		return fmt.Errorf("stack: embedding batch size %d must be positive", p.EmbeddingBatchSize)
	case strings.TrimSpace(p.BridgeListenTarget) == "":
		return errors.New("stack: a bridge listen target is required")
	}

	for _, pin := range []struct {
		what  string
		image string
	}{
		{what: "vector store image", image: p.VectorStoreImage},
		{what: "ollama image", image: p.OllamaImage},
	} {
		if err := validatePinnedImage(pin.what, pin.image); err != nil {
			return err
		}
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

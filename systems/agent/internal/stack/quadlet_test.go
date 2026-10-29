package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestQuadletHealthchecksMatchCompose(t *testing.T) {
	raw, err := os.ReadFile("../../../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Healthcheck struct {
				Test        []string `yaml:"test"`
				Interval    string   `yaml:"interval"`
				Timeout     string   `yaml:"timeout"`
				Retries     int      `yaml:"retries"`
				StartPeriod string   `yaml:"start_period"`
			} `yaml:"healthcheck"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []Profile{testProfile(), hostedProfile()} {
		units := rendered(t, profile)
		for name, unit := range units {
			if !strings.HasSuffix(name, ".container") {
				continue
			}
			service := "q15-" + strings.TrimSuffix(
				strings.TrimPrefix(name, profile.ResourceName()+"-"),
				".container",
			)
			health := compose.Services[service].Healthcheck
			if len(health.Test) == 0 {
				if len(unit["HealthCmd"]) != 0 || len(unit["ExecStartPost"]) != 0 ||
					len(unit["Notify"]) != 0 {
					t.Fatalf("%s has a health gate without a Compose probe", name)
				}
				continue
			}
			command := single(t, unit, "HealthCmd")
			if health.Test[0] == "CMD-SHELL" {
				if command != health.Test[1] {
					t.Fatalf("%s health command = %q, want %q", name, command, health.Test[1])
				}
			} else {
				var args []string
				if err := json.Unmarshal([]byte(command), &args); err != nil || strings.Join(args, "\n") != strings.Join(health.Test, "\n") {
					t.Fatalf("%s health command = %q, want %v (error=%v)", name, command, health.Test, err)
				}
			}
			want := map[string]string{
				"HealthInterval": health.Interval, "HealthTimeout": health.Timeout,
				"HealthRetries": fmt.Sprint(health.Retries), "Notify": "healthy",
				"TimeoutStartSec": "15min", "RestartSec": "10s",
			}
			if health.StartPeriod != "" {
				want["HealthStartPeriod"] = health.StartPeriod
			}
			for key, value := range want {
				if got := single(t, unit, key); got != value {
					t.Fatalf("%s %s = %q, want %q", name, key, got, value)
				}
			}
			gate := single(t, unit, "ExecStartPost")
			if !strings.Contains(
				gate,
				"podman healthcheck run "+single(t, unit, "ContainerName")+" ",
			) {
				t.Fatalf("%s gate checks a different container: %s", name, gate)
			}
		}
	}
}

func TestQuadletStartupGateWaitsForHealth(t *testing.T) {
	dir := t.TempDir()
	probe := "#!/bin/sh\nprintf 'probe\\n' >> attempts\ntest -f healthy\n"
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(probe), 0o700); err != nil {
		t.Fatal(err)
	}
	unit := rendered(t, testProfile())["q15-agent.container"]
	gate := single(t, unit, "ExecStartPost")
	script := strings.TrimSuffix(strings.TrimPrefix(gate, "/bin/sh -ec '"), "'")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-ec", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	for {
		if _, err := os.Stat(filepath.Join(dir, "attempts")); err == nil {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("gate finished before the container was healthy: %v", err)
		case <-ctx.Done():
			t.Fatal("gate did not run the health probe")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case err := <-finished:
		t.Fatalf("gate finished while unhealthy: %v", err)
	default:
	}
	if err := os.WriteFile(filepath.Join(dir, "healthy"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestBindMountsAreReadOnlyAtConfiguredPaths(t *testing.T) {
	units := rendered(t, testProfile())
	want := map[string][]string{
		"q15-proxy.container": {"/etc/q15/proxy/policy.yaml"},
		"q15-agent.container": {"/etc/q15/agent/config.yaml"},
	}
	for name, targets := range want {
		mounts := units[name]["Mount"]
		for _, target := range targets {
			found := false
			for _, mount := range mounts {
				options := strings.Split(mount, ",")
				var destination string
				readOnly := false
				for _, option := range options {
					if value, ok := strings.CutPrefix(option, "destination="); ok {
						destination = value
					}
					if option == "ro" {
						readOnly = true
					}
				}
				if destination == target && readOnly {
					found = true
				}
			}
			if !found {
				t.Errorf("%s has no read-only bind at %s: %v", name, target, mounts)
			}
		}
	}
}

func TestUnitsMountSecretsThroughPodman(t *testing.T) {
	units := rendered(t, testProfile())
	proxy := units["q15-proxy.container"]
	if got := proxy["Secret"]; len(got) != 1 ||
		got[0] != "github_token,type=mount,uid=0,gid=0,mode=0400" {
		t.Fatalf("proxy secrets = %v, want its token as a private Podman secret mount", got)
	}
	if got := environment(t, proxy, "GITHUB_TOKEN_FILE"); got != "/run/secrets/github_token" {
		t.Fatalf("GITHUB_TOKEN_FILE = %q, want the Podman secret path", got)
	}
	if got := units["q15-agent.container"]["Secret"]; len(got) != 1 ||
		got[0] != "q15_postgres_password,type=mount,uid=0,gid=0,mode=0400" {
		t.Fatalf("keyless chat profile still needs its database secret: %v", got)
	}
	for name, unit := range units {
		for _, mount := range unit["Mount"] {
			if strings.Contains(mount, "/secrets,") {
				t.Errorf("%s bind-mounts the payload store: %s", name, mount)
			}
		}
	}
}

// TestUnitsCoverTheStackAndCarryItsName is the naming contract: every unit, the
// container inside it and the network they join carry the stack's name, so two
// stacks on one host cannot collide.
func TestUnitsCoverTheStackAndCarryItsName(t *testing.T) {
	units := rendered(t, testProfile())

	want := []string{
		"q15-agent.container",
		"q15-exec.container",
		"q15-net.network",
		"q15-ollama.container",
		"q15-postgres.container",
		"q15-proxy.container",
		"q15-qdrant.container",
		"q15-web.container",
	}
	got := make([]string, 0, len(units))
	for name := range units {
		got = append(got, name)
		if !strings.HasPrefix(name, "q15-") {
			t.Fatalf("unit %q does not carry the stack name", name)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rendered units = %v, want %v", got, want)
	}

	if name := single(t, units["q15-net.network"], "NetworkName"); name != "q15-net" {
		t.Fatalf("NetworkName = %q, want %q", name, "q15-net")
	}

	for name, unit := range units {
		if !strings.HasSuffix(name, ".container") {
			continue
		}
		// The container name is the DNS name the agent, the web client and the
		// CLI's own endpoints use, so it is not left to podman's default.
		if got, want := single(
			t,
			unit,
			"ContainerName",
		), strings.TrimSuffix(
			name,
			".container",
		); got != want {
			t.Fatalf("%s: ContainerName = %q, want %q", name, got, want)
		}
		if got := single(t, unit, "Network"); got != "q15-net.network" {
			t.Fatalf("%s: Network = %q, want the stack's network unit", name, got)
		}
		if got := single(t, unit, "Restart"); got != "always" {
			t.Fatalf("%s: Restart = %q, want systemd to own the restart policy", name, got)
		}
	}
}

// TestImageTagsAreTheReleaseAndNothingFloating is the rule that keeps a
// restarted service from silently changing version under the operator.
func TestImageTagsAreTheReleaseAndNothingFloating(t *testing.T) {
	units := rendered(t, testProfile())

	for _, service := range []string{"agent", "exec", "proxy", "web"} {
		unit := units["q15-"+service+".container"]
		want := "ghcr.io/q15co/q15-" + service + ":" + testRelease
		if got := single(t, unit, "Image"); got != want {
			t.Fatalf("q15-%s image = %q, want %q", service, got, want)
		}
	}

	floating := []string{":main", ":stable", ":latest", ":dev", ":edge", ":nightly", ":master"}
	for name, unit := range units {
		for _, image := range unit["Image"] {
			if !strings.Contains(image, ":") {
				t.Fatalf("%s: image %q carries no tag at all", name, image)
			}
			for _, tag := range floating {
				if strings.Contains(image, tag) {
					t.Fatalf("%s: image %q carries the floating tag %q", name, image, tag)
				}
			}
		}
	}
}

// TestVectorStoreIsPinnedOnItsOwnAxis keeps the vector store out of the release
// train: a collection that already exists is not repinned by a CLI upgrade.
func TestVectorStoreIsPinnedOnItsOwnAxis(t *testing.T) {
	profile := testProfile()
	units := rendered(t, profile)
	if got := single(t, units["q15-qdrant.container"], "Image"); got != VectorStoreImage {
		t.Fatalf("vector store image = %q, want %q", got, VectorStoreImage)
	}
	if got := single(t, units["q15-ollama.container"], "Image"); got != OllamaImage {
		t.Fatalf("ollama image = %q, want %q", got, OllamaImage)
	}

	profile.Release = "9.9.9"
	units = rendered(t, profile)
	if got := single(t, units["q15-qdrant.container"], "Image"); got != VectorStoreImage {
		t.Fatalf(
			"after a release move the vector store image = %q, want it to stay %q",
			got,
			VectorStoreImage,
		)
	}
	if got := single(t, units["q15-ollama.container"], "Image"); got != OllamaImage {
		t.Fatalf("after a release move the ollama image = %q, want it to stay %q", got, OllamaImage)
	}
	if got := single(t, units["q15-agent.container"], "Image"); !strings.HasSuffix(got, ":9.9.9") {
		t.Fatalf("after a release move the agent image = %q, want the new release", got)
	}
}

// TestRenderedUnitsKeepTheComposeInvariants is the contract the Compose files
// already pin, restated where the units can be read without a running stack.
func TestRenderedUnitsKeepTheComposeInvariants(t *testing.T) {
	units := rendered(t, testProfile())

	web := units["q15-web.container"]
	if got := single(t, web, "User"); got != "65532:1000" {
		t.Fatalf("web User = %q, want the unprivileged image user in the host group", got)
	}
	if got := single(t, web, "PublishPort"); got != "127.0.0.1:8080:8080" {
		t.Fatalf("web PublishPort = %q, want loopback only", got)
	}
	if got := environment(t, web, "Q15_WEB_STATE_DIR"); got != "/var/lib/q15-web" {
		t.Fatalf("web Q15_WEB_STATE_DIR = %q, want %q", got, "/var/lib/q15-web")
	}
	if got := environment(t, web, "Q15_WEB_BRIDGE"); got != "unix:///run/q15/bridge.sock" {
		t.Fatalf("web Q15_WEB_BRIDGE = %q, want the bridge socket", got)
	}
	if hasEnvironment(web, "Q15_WEB_TOKEN") {
		t.Fatal("web is rendered a token: the browser tier holds no credential")
	}

	if got := single(t, units["q15-agent.container"], "GroupAdd"); got != "1000" {
		t.Fatalf("agent GroupAdd = %q, want the group that owns the bridge socket", got)
	}

	// The socket volume is mounted at one path, writable by the agent and
	// read-only for the browser client.
	webBridge := volumeMount(t, web, "q15_bridge")
	if webBridge != "q15_bridge:/run/q15:ro" {
		t.Fatalf("web bridge mount = %q, want it read-only at /run/q15", webBridge)
	}
	if got := volumeMount(t, units["q15-exec.container"], "q15_memory"); got != "q15_memory:/memory:ro" {
		t.Fatalf("exec memory mount = %q, want read-only", got)
	}
	if got := volumeMount(t, units["q15-agent.container"], "q15_memory"); got != "q15_memory:/memory" {
		t.Fatalf("agent memory mount = %q, want writable", got)
	}
	agentBridge := volumeMount(t, units["q15-agent.container"], "q15_bridge")
	if agentBridge != "q15_bridge:/run/q15" {
		t.Fatalf("agent bridge mount = %q, want it writable at /run/q15", agentBridge)
	}
}

// TestVolumesAdoptTheComposeDeployment reads the volume names out of the Compose
// files the repository ships and insists the default stack renders those exact
// names. Anything else would adopt an existing deployment and start it with
// empty memory while the old data sat in a volume nobody mounts.
func TestVolumesAdoptTheComposeDeployment(t *testing.T) {
	composeVolumes := volumesInComposeFiles(t)
	units := rendered(t, testProfile())

	renderedVolumes := map[string]bool{}
	for _, unit := range units {
		for _, mount := range unit["Volume"] {
			source, _, _ := strings.Cut(mount, ":")
			renderedVolumes[source] = true
		}
	}

	// The deprecated embedding server's cache: the install profile serves
	// embeddings from ollama instead, and that server is removed one release
	// after the profile ships, so its volume is deliberately not rendered.
	deliberatelyNotRendered := map[string]bool{"q15_tei_hf_cache": true}

	// The volume the profile adds for the model server the Compose files do not
	// run yet. A second new name has to be added here on purpose, because a typo
	// in a volume name is otherwise silent: the stack starts, and it starts
	// empty.
	addedByTheProfile := map[string]bool{"q15_ollama_models": true}

	compose := make(map[string]bool, len(composeVolumes))
	for _, name := range composeVolumes {
		compose[name] = true
		if deliberatelyNotRendered[name] {
			continue
		}
		if !renderedVolumes[name] {
			t.Fatalf(
				"the Compose volume %q is not rendered, so adopting this stack would start with an empty volume",
				name,
			)
		}
	}

	for name := range renderedVolumes {
		if compose[name] || addedByTheProfile[name] {
			continue
		}
		t.Fatalf("the stack renders the volume %q, which neither Compose file names", name)
	}
}

// TestASecondStackSharesNoNames is the reservation multi-stack support rests on:
// a second stack name renders a set that touches nothing the first owns.
func TestASecondStackSharesNoNames(t *testing.T) {
	first := resourceNames(t, testProfile())
	second := resourceNames(t, func() Profile {
		profile := testProfile()
		profile.AgentName = "Demo Stack"
		return profile
	}())

	if len(second) == 0 {
		t.Fatal("the second stack rendered no names")
	}
	owned := make(map[string]bool, len(first))
	for _, name := range first {
		owned[name] = true
	}
	for _, name := range second {
		if owned[name] {
			t.Fatalf("the second stack renders %q, which the first stack already owns", name)
		}
	}
}

// TestUnitsAreDeterministic keeps two renders of one profile byte-identical,
// which is what makes a re-render a no-op rather than a churn.
func TestUnitsAreDeterministic(t *testing.T) {
	first, err := testProfile().Units()
	if err != nil {
		t.Fatalf("Units() error = %v", err)
	}
	second, err := testProfile().Units()
	if err != nil {
		t.Fatalf("Units() error = %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("two renders produced %d and %d units", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("unit %d is %q and then %q", i, first[i].Name, second[i].Name)
		}
		if !bytes.Equal(first[i].Contents, second[i].Contents) {
			t.Fatalf("unit %s rendered differently on the second call", first[i].Name)
		}
	}
}

// TestUnitsRefuseABuildThatIsNotARelease states the sharpest form of the pinning
// rule: a development build has no release, so it has no image tag to write, and
// rendering it would produce units that pull nothing.
func TestUnitsRefuseABuildThatIsNotARelease(t *testing.T) {
	profile := DefaultProfile(unReleasedBuild, t.TempDir())

	_, err := profile.Units()
	if err == nil {
		t.Fatal("Units() error = nil, want a refusal for a build that is not a release")
	}
	if !strings.Contains(err.Error(), unReleasedBuild) {
		t.Fatalf("Units() error = %v, want it to name the release", err)
	}
	if _, err := profile.Render(t.TempDir()); err == nil {
		t.Fatal("Render() error = nil, want a refusal for a build that is not a release")
	}
}

// TestAgentPreparesNoCredentialTheProfileDoesNotDeclare states the rule the units
// follow: they carry the credentials the profile names and no others. The Compose
// example passes five provider and tool keys, which belong to whoever wrote it; a
// rendered stack must not look like it is waiting for somebody else's.
func TestAgentPreparesNoCredentialTheProfileDoesNotDeclare(t *testing.T) {
	units := rendered(t, testProfile())

	for _, entry := range units["q15-agent.container"]["Environment"] {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasSuffix(name, "_FILE") && name != "PGPASSWORD_FILE" {
			t.Fatalf("the agent unit prepares %s, which the profile does not declare", name)
		}
	}

	// The proxy is the one exception, and its pointer follows its policy: the stock
	// policy declares the github_token secret, so the unit is where the proxy finds
	// it. That is also why the default policy's demand for a token is an issue of
	// its own rather than something the units can paper over.
	if got := environment(
		t,
		units["q15-proxy.container"],
		"GITHUB_TOKEN_FILE",
	); got != "/run/secrets/github_token" {
		t.Fatalf("proxy GITHUB_TOKEN_FILE = %q, want it under the secrets directory", got)
	}
}

// TestHostedProfilePreparesTheCredentialsItNames is the other half of the rule
// about credentials: every declared key gets a pointer and a Podman secret mount.
func TestHostedProfilePreparesTheCredentialsItNames(t *testing.T) {
	agent := rendered(t, hostedProfile())["q15-agent.container"]

	want := map[string]string{
		"Q15_CHAT_API_KEY_FILE":  "/run/secrets/q15_chat_api_key",
		"Q15_EMBED_API_KEY_FILE": "/run/secrets/q15_embed_api_key",
		"QDRANT_API_KEY_FILE":    "/run/secrets/qdrant_api_key",
	}
	for name, want := range want {
		if got := environment(t, agent, name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}

	// The endpoints arrive the way a local stack's do: named once, in the unit,
	// and read out of the environment by the agent.
	if got, want := environment(
		t,
		agent,
		embeddingsURLEnv,
	), "https://embeddings.example.net/v1"; got != want {
		t.Fatalf("%s = %q, want %q", embeddingsURLEnv, got, want)
	}
	if got, want := environment(
		t,
		agent,
		qdrantURLEnv,
	), "https://vectors.example.net:6334"; got != want {
		t.Fatalf("%s = %q, want %q", qdrantURLEnv, got, want)
	}

	wantSecrets := []string{
		"q15_chat_api_key,type=mount,uid=0,gid=0,mode=0400",
		"q15_embed_api_key,type=mount,uid=0,gid=0,mode=0400",
		"qdrant_api_key,type=mount,uid=0,gid=0,mode=0400",
		"ollama_api_key,type=mount,uid=0,gid=0,mode=0400",
		"q15_postgres_password,type=mount,uid=0,gid=0,mode=0400",
	}
	if got := agent["Secret"]; strings.Join(got, "\n") != strings.Join(wantSecrets, "\n") {
		t.Fatalf("agent secrets = %v, want %v", got, wantSecrets)
	}

}

// resourceNames is every name one stack owns: its units, the containers inside
// them, its network and its volumes.
func resourceNames(t *testing.T, profile Profile) []string {
	t.Helper()
	units := rendered(t, profile)
	names := make([]string, 0, len(units)*3)
	for name, unit := range units {
		names = append(names, name)
		names = append(names, unit["ContainerName"]...)
		names = append(names, unit["NetworkName"]...)
		for _, mount := range unit["Volume"] {
			source, _, _ := strings.Cut(mount, ":")
			names = append(names, source)
		}
	}
	return names
}

// volumesInComposeFiles reads the volume names out of the Compose files this
// repository ships, relative to the package directory `go test` runs in.
func volumesInComposeFiles(t *testing.T) []string {
	t.Helper()
	paths := []string{
		"../../../../docker-compose.yml",
		"../../../../deploy/compose/docker-compose.image-first.yml",
	}

	var names []string
	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var compose struct {
			Volumes map[string]any `yaml:"volumes"`
		}
		if err := yaml.Unmarshal(raw, &compose); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for name := range compose.Volumes {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		t.Fatal("no volumes found in the Compose files, want the shipped deployment")
	}
	return names
}

// rendered renders the profile and parses each unit into its directives.
func rendered(t *testing.T, profile Profile) map[string]map[string][]string {
	t.Helper()
	units, err := profile.Units()
	if err != nil {
		t.Fatalf("Units() error = %v", err)
	}
	out := make(map[string]map[string][]string, len(units))
	for _, unit := range units {
		out[unit.Name] = directives(t, unit.Contents)
	}
	return out
}

// directives parses one rendered unit into its keys and values, so a test
// asserts the contract rather than a formatting detail.
func directives(t *testing.T, contents []byte) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("unit line %q is not key=value", line)
		}
		out[strings.TrimSpace(key)] = append(out[strings.TrimSpace(key)], strings.TrimSpace(value))
	}
	return out
}

// single is the one value a key that must appear exactly once holds.
func single(t *testing.T, unit map[string][]string, key string) string {
	t.Helper()
	values := unit[key]
	if len(values) != 1 {
		t.Fatalf("unit has %d %s= lines, want exactly one (%v)", len(values), key, values)
	}
	return values[0]
}

// volumeMount is the mount of a named volume, wherever in the unit it appears.
func volumeMount(t *testing.T, unit map[string][]string, volume string) string {
	t.Helper()
	for _, mount := range unit["Volume"] {
		if strings.HasPrefix(mount, volume+":") {
			return mount
		}
	}
	t.Fatalf("unit mounts no volume %q (%v)", volume, unit["Volume"])
	return ""
}

// environment is the value of one container environment variable, which a unit
// writes as Environment=KEY=VALUE.
func environment(t *testing.T, unit map[string][]string, key string) string {
	t.Helper()
	for _, entry := range unit["Environment"] {
		if name, value, ok := strings.Cut(entry, "="); ok && name == key {
			return value
		}
	}
	t.Fatalf("unit sets no %s (%v)", key, unit["Environment"])
	return ""
}

// hasEnvironment reports whether a unit sets an environment variable at all.
func hasEnvironment(unit map[string][]string, key string) bool {
	for _, entry := range unit["Environment"] {
		if name, _, ok := strings.Cut(entry, "="); ok && name == key {
			return true
		}
	}
	return false
}

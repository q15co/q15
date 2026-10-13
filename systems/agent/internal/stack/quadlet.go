package stack

import (
	"fmt"
	"sort"
	"strings"
)

// Unit is one rendered Quadlet file and its contents.
type Unit struct {
	Name     string
	Contents []byte
}

// mount is one filesystem a container sees.
type mount struct {
	// source is either a podman volume the stack owns or a path on the host.
	source string
	// target is the path inside the container.
	target string
	// readOnly mounts the source read-only.
	readOnly bool
}

// readOnlyMount returns the mount marked read-only.
func (m mount) readOnlyMount() mount {
	m.readOnly = true
	return m
}

// container is one service of the stack.
type container struct {
	// service is the name's last element: q15-<service> is the unit, the
	// container and the DNS name other containers use.
	service string
	// description is what systemd shows for the unit.
	description string
	// image is the reference written literally into the unit.
	image string
	// user is the container user, when the image does not pick one itself.
	user string
	// groupAdd are the supplementary groups the container's process gets.
	groupAdd []string
	// environment is written in this order, so two renders are comparable.
	environment [][2]string
	// volumes and hostMounts are what the container sees, in this order.
	volumes    []mount
	hostMounts []mount
	secrets    []string
	// publishPorts are host bindings; all of them are loopback.
	publishPorts []string
	// requires are the services this one needs running first.
	requires []string
	// enabled is true for the one unit the stack hangs off at boot: enabling it
	// is what makes the whole stack come back after a reboot, because the rest
	// are pulled in as its requirements.
	enabled bool
}

// Units renders the profile as the units a stack consists of: one container unit
// per service, and the network they all join. The result is sorted by unit name,
// so two renders of the same profile are byte-identical.
func (p Profile) Units() ([]Unit, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	containers := p.containers()
	units := make([]Unit, 0, len(containers)+1)
	for _, c := range containers {
		units = append(units, Unit{
			Name:     p.unitName(c.service),
			Contents: p.renderContainer(c),
		})
	}
	units = append(units, Unit{
		Name:     p.networkUnitName(),
		Contents: p.renderNetwork(),
	})

	sort.Slice(units, func(i, j int) bool { return units[i].Name < units[j].Name })
	return units, nil
}

// UnitNames returns the names Units renders, sorted, without rendering them.
func (p Profile) UnitNames() []string {
	names := make([]string, 0, len(p.containers())+1)
	for _, c := range p.containers() {
		names = append(names, p.unitName(c.service))
	}
	names = append(names, p.networkUnitName())
	sort.Strings(names)
	return names
}

// ServiceNames returns the systemd services generated from the Quadlet files.
func (p Profile) ServiceNames() []string {
	names := make([]string, 0, len(p.containers())+1)
	for _, c := range p.containers() {
		names = append(names, p.resource(c.service)+".service")
	}
	// Quadlet adds a type suffix to generated network services.
	names = append(names, p.NetworkName()+"-network.service")
	sort.Strings(names)
	return names
}

// unitName is the Quadlet file name of one container.
func (p Profile) unitName(service string) string {
	return p.resource(service) + ".container"
}

// networkUnitName is the file name of the network unit.
func (p Profile) networkUnitName() string {
	return p.NetworkName() + ".network"
}

// image is the reference for one of the four q15 images: their tag is the
// release this CLI was built from, and nothing else.
func (p Profile) image(service string) string {
	return releaseImageRepository + "/q15-" + service + ":" + p.Release
}

// volume is a mount of a volume the stack owns.
func (p Profile) volume(suffix, target string) mount {
	return mount{source: p.volumeName(suffix), target: target}
}

// hostPath is a mount of a file or directory the CLI rendered into the state
// directory. Bind mounts rather than volumes on purpose: a volume would create
// an empty directory where a file is missing, and a stack whose configuration
// was never written should fail loudly instead.
func (p Profile) hostPath(path, target string) mount {
	return mount{source: p.StateDir + "/" + path, target: target}
}

// containers is the profile's container set, in start order.
//
// The two model servers belong to the set only when this profile runs them. An
// external source is a server somebody else runs, and a stack that rendered one
// anyway would start a container nothing talks to.
func (p Profile) containers() []container {
	containers := []container{
		{
			service:     "proxy",
			description: "q15 egress proxy",
			image:       p.image("proxy"),
			environment: [][2]string{
				{"GITHUB_TOKEN_FILE", secretsDirTarget + "/" + githubTokenFile},
			},
			volumes: []mount{
				p.volume("proxy_state", "/var/lib/q15/proxy"),
			},
			hostMounts: []mount{
				p.hostPath("proxy-policy.yaml", proxyPolicyTarget).readOnlyMount(),
			},
			secrets: []string{githubTokenFile},
		},
		{
			service:     "exec",
			description: "q15 command execution",
			image:       p.image("exec"),
			environment: [][2]string{
				{"TZ", "UTC"},
			},
			volumes: []mount{
				p.volume("workspace", "/workspace"),
				p.volume("memory", "/memory"),
				p.volume("media", "/media"),
				p.volume("skills", "/skills"),
				p.volume("exec_nix_store", "/nix"),
			},
			requires: []string{"proxy"},
		},
	}

	if p.runsVectorStore() {
		containers = append(containers, container{
			service:     "qdrant",
			description: "q15 vector store",
			image:       p.VectorStoreImage,
			volumes: []mount{
				p.volume("qdrant_storage", "/qdrant/storage"),
			},
		})
	}
	if p.runsOllama() {
		containers = append(containers, container{
			service:     "ollama",
			description: "q15 model server",
			image:       p.OllamaImage,
			volumes: []mount{
				p.volume("ollama_models", "/root/.ollama"),
			},
		})
	}

	// The agent waits only for what is there: a requirement on a unit the
	// renderer did not write is a stack that will not start.
	agentRequires := []string{"exec"}
	if p.runsVectorStore() {
		agentRequires = append(agentRequires, "qdrant")
	}
	if p.runsOllama() {
		agentRequires = append(agentRequires, "ollama")
	}

	containers = append(containers, container{
		service:     "agent",
		description: "q15 agent",
		image:       p.image("agent"),
		// The bridge socket is owned by the host user's group, and the agent
		// writes into it, so it needs that group inside the container.
		groupAdd: []string{"1000"},
		// The endpoints are named once, here, and the agent's configuration reads
		// them from the environment rather than repeating the URLs. A credential
		// gets the same treatment: the profile declares the variable, the unit
		// points <NAME>_FILE at its Podman secret mount. A profile that declares
		// no credential prepares none.
		environment: append(
			[][2]string{
				{"HOME", "/root"},
				{"GODEBUG", "updatemaxprocs=0"},
				{"GOMAXPROCS", "1"},
				{"TZ", "UTC"},
				{qdrantURLEnv, p.QdrantURL()},
				{embeddingsURLEnv, p.EmbeddingsBaseURL()},
			},
			p.credentialPointers()...,
		),
		volumes: []mount{
			p.volume("workspace", "/workspace"),
			p.volume("memory", "/memory"),
			p.volume("media", "/media"),
			p.volume("skills", "/skills"),
			p.volume("agent_state", "/var/lib/q15/agent"),
			p.volume("bridge", "/run/q15"),
		},
		hostMounts: []mount{
			p.hostPath("agent-config.yaml", agentConfigTarget).readOnlyMount(),
			p.hostPath("auth", authDirTarget),
		},
		secrets:  p.credentialSecrets(),
		requires: agentRequires,
	})

	containers = append(containers, container{
		service:     "web",
		description: "q15 browser client",
		image:       p.image("web"),
		// The web tier holds no credential and owns no writable data outside
		// its own state, so it runs as the unprivileged image user in the
		// host user's group, which is what lets it use the bridge socket.
		user: "65532:1000",
		environment: [][2]string{
			{"Q15_WEB_LISTEN", "0.0.0.0:8080"},
			{"Q15_WEB_BRIDGE", p.BridgeListenTarget},
			{"Q15_WEB_ORIGIN", fmt.Sprintf("http://localhost:%d", p.HostPort)},
			{"Q15_WEB_STATE_DIR", "/var/lib/q15-web"},
		},
		volumes: []mount{
			p.volume("web_state", "/var/lib/q15-web"),
			// Read-only: the bridge socket is the agent's, and the browser
			// client only speaks over it.
			p.volume("bridge", "/run/q15").readOnlyMount(),
		},
		publishPorts: []string{fmt.Sprintf("127.0.0.1:%d:8080", p.HostPort)},
		requires:     []string{"agent"},
		enabled:      true,
	})
	return containers
}

func (p Profile) credentialSecrets() []string {
	pointers := p.credentialPointers()
	names := make([]string, 0, len(pointers))
	for _, pointer := range pointers {
		names = append(names, strings.TrimPrefix(pointer[1], secretsDirTarget+"/"))
	}
	return names
}

// renderContainer writes one container unit. The header says where the file came
// from, because it is a rendered file that a later render will overwrite.
func (p Profile) renderContainer(c container) []byte {
	var b strings.Builder
	fmt.Fprintf(&b,
		"# Rendered by q15 %s from the %s stack's profile. Edit the profile, not this file.\n\n",
		p.Release, p.ResourceName())

	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=%s (%s)\n", c.description, p.ResourceName())
	// The network is a unit of its own, so the container has to wait for it.
	fmt.Fprintf(&b, "After=%s\n", p.networkUnitName())
	fmt.Fprintf(&b, "Requires=%s\n", p.networkUnitName())
	for _, service := range c.requires {
		fmt.Fprintf(&b, "After=%s\n", p.unitName(service))
		fmt.Fprintf(&b, "Requires=%s\n", p.unitName(service))
	}

	b.WriteString("\n[Container]\n")
	// Named explicitly: without this, podman would call the container
	// systemd-<unit>, and the other containers reach it by name.
	fmt.Fprintf(&b, "ContainerName=%s\n", p.resource(c.service))
	fmt.Fprintf(&b, "Image=%s\n", c.image)
	fmt.Fprintf(&b, "Network=%s\n", p.networkUnitName())
	for _, m := range c.volumes {
		fmt.Fprintf(&b, "Volume=%s\n", formatMount(m))
	}
	for _, m := range c.hostMounts {
		fmt.Fprintf(&b, "Mount=type=bind,source=%s,destination=%s", m.source, m.target)
		if m.readOnly {
			b.WriteString(",ro")
		}
		b.WriteByte('\n')
	}
	for _, name := range c.secrets {
		fmt.Fprintf(&b, "Secret=%s,type=mount,uid=0,gid=0,mode=0400\n", name)
	}
	for _, port := range c.publishPorts {
		fmt.Fprintf(&b, "PublishPort=%s\n", port)
	}
	if c.user != "" {
		fmt.Fprintf(&b, "User=%s\n", c.user)
	}
	for _, group := range c.groupAdd {
		fmt.Fprintf(&b, "GroupAdd=%s\n", group)
	}
	for _, env := range c.environment {
		fmt.Fprintf(&b, "Environment=%s=%s\n", env[0], env[1])
	}

	// systemd owns the restart policy; podman's own --restart is deliberately not
	// used, because two restarters would fight over the same container.
	b.WriteString("\n[Service]\nRestart=always\n")

	if c.enabled {
		b.WriteString("\n[Install]\nWantedBy=default.target\n")
	}
	return []byte(b.String())
}

// renderNetwork writes the one network the stack's containers share.
func (p Profile) renderNetwork() []byte {
	var b strings.Builder
	fmt.Fprintf(&b,
		"# Rendered by q15 %s from the %s stack's profile. Edit the profile, not this file.\n\n",
		p.Release, p.ResourceName())
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=q15 stack network (%s)\n", p.ResourceName())
	b.WriteString("\n[Network]\n")
	// Named explicitly so the network is not called systemd-<unit>; nothing
	// depends on the name, but a person reading podman's output should see the
	// stack's own name.
	fmt.Fprintf(&b, "NetworkName=%s\n", p.NetworkName())
	return []byte(b.String())
}

// formatMount renders a volume mount as podman's -v form.
func formatMount(m mount) string {
	return m.source + ":" + m.target + readOnlySuffix(m)
}

// readOnlySuffix marks a mount read-only.
func readOnlySuffix(m mount) string {
	if m.readOnly {
		return ":ro"
	}
	return ""
}

// credentialPointers renders every credential the profile declares as the
// <NAME>_FILE variable the agent resolves it from, pointing into the secrets
// directory Podman mounts them under. The secret name is the variable name in
// lower case, matching the name supplied to q15 secret set.
func (p Profile) credentialPointers() [][2]string {
	declared := []string{p.Chat.KeyEnv, p.Embeddings.KeyEnv, p.Vectors.KeyEnv}
	pointers := make([][2]string, 0, len(declared))
	seen := make(map[string]bool, len(declared))
	for _, keyEnv := range declared {
		keyEnv = strings.TrimSpace(keyEnv)
		if keyEnv == "" {
			continue
		}
		// One variable named twice is one pointer, not two identical ones.
		if seen[keyEnv] {
			continue
		}
		seen[keyEnv] = true
		pointers = append(pointers, [2]string{
			keyEnv + "_FILE",
			secretsDirTarget + "/" + secretFileName(keyEnv),
		})
	}
	return pointers
}

// secretFileName is the file one credential's value is read from: the variable
// name in lower case, with anything that is not a letter or a digit turned into
// an underscore, so the name is safe as a file name wherever it lands.
func secretFileName(keyEnv string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(keyEnv)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

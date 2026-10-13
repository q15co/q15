package stack

import (
	"fmt"
	"sort"
	"strings"
)

// Unit is one rendered Quadlet file: the name it is written under, which is the
// unit name systemd reports, and its contents.
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

// unitName is the file name, and therefore the unit name, of one service.
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
func (p Profile) containers() []container {
	return []container{
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
				p.hostPath("secrets", secretsDirTarget).readOnlyMount(),
			},
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
		{
			service:     "qdrant",
			description: "q15 vector store",
			image:       p.VectorStoreImage,
			volumes: []mount{
				p.volume("qdrant_storage", "/qdrant/storage"),
			},
		},
		{
			service:     "ollama",
			description: "q15 model server",
			image:       p.OllamaImage,
			volumes: []mount{
				p.volume("ollama_models", "/root/.ollama"),
			},
		},
		{
			service:     "agent",
			description: "q15 agent",
			image:       p.image("agent"),
			// The bridge socket is owned by the host user's group, and the agent
			// writes into it, so it needs that group inside the container.
			groupAdd: []string{"1000"},
			// No credential pointer: the profile declares one provider, the
			// model server beside it, and a local model server takes no key. A
			// stack configured with a hosted provider gets
			// <KEY_ENV>_FILE for that provider once the profile says which one it
			// is, which is what keeps the units and the configuration from
			// describing two different stacks.
			environment: [][2]string{
				{"HOME", "/root"},
				{"GODEBUG", "updatemaxprocs=0"},
				{"GOMAXPROCS", "1"},
				{"TZ", "UTC"},
				{qdrantURLEnv, p.QdrantURL()},
				{embeddingsURLEnv, p.EmbeddingsBaseURL()},
			},
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
				p.hostPath("secrets", secretsDirTarget).readOnlyMount(),
			},
			requires: []string{"exec", "qdrant", "ollama"},
		},
		{
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
		},
	}
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

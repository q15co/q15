# q15 Kubernetes Base

This base is intended to be consumed by a separate deployment repository as a single q15 stack base
that gets instantiated once per namespace.

It owns:

- Deployments and Services for `q15-agent`, `q15-exec`, and `q15-proxy`
- ConfigMap-backed example files for structured agent config and proxy policy
- Stable namespace-scoped resource names that overlays can patch with labels, namespaces, and
  environment-specific config

It does not own:

- Cluster-specific overlays
- PersistentVolumeClaim definitions
- Secret material

The runtime contract is fixed in the binaries. Overlays only need to provide:

- `ConfigMap/q15-agent-config` data matching `agent-config.yaml`
- `ConfigMap/q15-proxy-policy` data matching `proxy-policy.yaml`
- `Secret/q15-agent-env` with keys referenced by the agent config
  - Example: `MOONSHOT_API_KEY`, `JARED_TELEGRAM_TOKEN`
- `Secret/q15-agent-auth` with key `auth.json`
- `Secret/q15-proxy-env` with keys matching the uppercased proxy secret aliases in
  `proxy-policy.yaml`
  - Example: `JARED_GH_TOKEN`
- PVCs named `q15-workspace`, `q15-memory`, `q15-skills`, `q15-exec-nix`, and `q15-proxy-state`

The supported Kubernetes topology is one namespace per q15 stack. Within that namespace, one stack
contains:

- one `q15-agent`
- one `q15-exec`
- one `q15-proxy`
- stack-local config and secret inputs for agent config, proxy policy, `auth.json`, provider or API
  keys, and runtime tokens
- stack-owned PVCs for `/workspace`, `/memory`, `/skills`, `/nix`, and `/var/lib/q15/proxy`

The namespace is the isolation boundary. The checked-in base already encodes one pod for each
runtime service with `replicas: 1`, and it uses namespace-scoped Service names `q15-exec` and
`q15-proxy` for in-stack service discovery.

This matches the current exec runtime semantics: `q15-exec` supports multiple concurrent sessions
inside one pod, but session state is currently in-memory and pod-local. Keeping one exec pod per
stack avoids cross-pod session routing or sticky-session requirements, and stack-local volumes keep
storage ownership straightforward.

The base defaults to the moving GHCR `:main` tags:

- `ghcr.io/q15co/q15-agent:main`
- `ghcr.io/q15co/q15-exec:main`
- `ghcr.io/q15co/q15-proxy:main`

Typical overlay responsibilities:

- Choose the namespace for one stack instance
- Pin image names and tags with `images`
- Replace the generated config files or patch them with environment-specific values
- Provide Secret material for that stack
- Define the required PVCs, including StorageClasses and access modes

# q15 Kubernetes Base

This base is intended to be consumed by a separate deployment repository.

It owns:

- Deployments and Services for `q15-agent`, `q15-exec`, and `q15-proxy`
- ConfigMap-backed example files for structured agent config and proxy policy
- Stable resource names that overlays can patch with labels, namespaces, and environment-specific
  config

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

The base defaults to the moving GHCR `:main` tags:

- `ghcr.io/q15co/q15-agent:main`
- `ghcr.io/q15co/q15-exec:main`
- `ghcr.io/q15co/q15-proxy:main`

Typical overlay responsibilities:

- Pin image names and tags with `images`
- Replace the generated config files or patch them with environment-specific values
- Attach StorageClasses and access modes to the required PVCs

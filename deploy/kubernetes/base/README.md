# q15 Kubernetes Base

This base is intended to be consumed by a separate deployment repository as a single q15 stack base
that gets instantiated once per namespace.

For the canonical runtime storage/config/secret contract (including requiredness by Kubernetes vs
Compose and local-dev exceptions), see the top-level
[Storage Contract (Canonical)](../../../README.md#storage-contract-canonical).

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
  - Example: `MOONSHOT_API_KEY`, `BRAVE_API_KEY`, `Q15_TELEGRAM_TOKEN`,
    `Q15_TELEGRAM_ALLOWED_USER_IDS`
- `Secret/q15-agent-auth` with key `auth.json`
- `Secret/q15-proxy-env` with keys matching the uppercased proxy secret aliases in
  `proxy-policy.yaml`
  - Example: `GITHUB_TOKEN`
- PVCs named `q15-workspace`, `q15-memory`, `q15-skills`, `q15-agent-state`, `q15-exec-nix`, and
  `q15-proxy-state`

Provider discovery is mandatory: provider rosters are the source of truth for available models and
capabilities. The interactive and cognition models are runtime state, persisted under
`/workspace/.q15/agent/selection.json` on the durable `q15-workspace` volume. On first run q15
auto-selects a first-eligible roster model (preferring a tool-calling model); the agent/user then
changes it at runtime with `list_providers`, `list_models`, `switch_model`, and
`switch_cognition_model` (per background cognition job). Switches survive restart. Current inference
is text-first; image-input and tool-calling requirement inference are staged for the corresponding
canonical request signals.

`q15-workspace` is the stack's long-term project and working-state PVC. A fresh
`PersistentVolumeClaim/q15-workspace` may be empty on first deployment; pre-seeding it is optional
and the empty initial state still satisfies the runtime contract. Operators should preserve that PVC
across restarts and redeployments and treat it as durable stack state for retention and backup
planning.

`q15-memory` is also durable stack state. On `q15-agent` startup, stored turn files under
`/memory/history/turns/` are eagerly upgraded to the latest transcript schema before replay.
Unreadable files are moved aside under `/memory/history/quarantine/`. The same persistent root also
holds core self-model files under `/memory/core/`, canonical semantic memory files at
`/memory/semantic/facts.md`, `/memory/semantic/preferences.md`, and `/memory/semantic/projects.md`,
the working layer under `/memory/working/` with the canonical prompt-visible active-state file at
`/memory/working/WORKING_MEMORY.md`, cognition maintenance state under `/memory/cognition/`, and
zettelkasten notebook folders under `/memory/notes/inbox/`, `/memory/notes/zettel/`, and
`/memory/notes/maps/`. The notes tree remains auxiliary notebook space, not implicit working memory.

`q15-agent-state` is private durable runtime state for `q15-agent`, mounted at `/var/lib/q15/agent`.
Scheduled-job definitions and append-only run provenance live below `/var/lib/q15/agent/schedule/`;
this state is intentionally separate from the Git-backed memory tree. Each job persists its own
`allowed_tools`, selected by the main agent when the job is created or updated; there is no
deployment-wide scheduled-job tool allow-list.

The supported Kubernetes topology is one namespace per q15 stack. Within that namespace, one stack
contains:

- one `q15-agent`
- one `q15-exec`
- one `q15-proxy`
- stack-local config and secret inputs for agent config, proxy policy, `auth.json`, provider or API
  keys, and runtime tokens
- stack-owned PVCs for `/workspace`, `/memory`, `/skills`, `/var/lib/q15/agent`, `/nix`, and
  `/var/lib/q15/proxy`, with `/workspace` carrying the stack's durable project and working state

The namespace is the isolation boundary. The checked-in base already encodes one pod for each
runtime service with `replicas: 1`, and it uses namespace-scoped Service names `q15-exec` and
`q15-proxy` for in-stack service discovery.

This matches the current exec runtime semantics: `q15-exec` supports multiple concurrent sessions
inside one pod, but session state is currently in-memory and pod-local. Keeping one exec pod per
stack avoids cross-pod session routing or sticky-session requirements, and stack-local volumes keep
storage ownership straightforward.

Canonical runtime images:

- `ghcr.io/q15co/q15-agent`
- `ghcr.io/q15co/q15-exec`
- `ghcr.io/q15co/q15-proxy`

Published runtime tags today:

- `main`
- `sha-<full-sha>`
- `sha-<short-sha>` (convenience alias)

The checked-in base keeps the moving `:main` tags as placeholders:

- `ghcr.io/q15co/q15-agent:main`
- `ghcr.io/q15co/q15-exec:main`
- `ghcr.io/q15co/q15-proxy:main`

Downstream overlays should resolve one `ghcr.io/q15co/q15-agent:release-main` stack release and
replace those placeholders with the three exact OCI digest references recorded in it. Use the
immutable `release-<full-sha>` record for an explicit rollout or rollback. Treat `main` as a
fast-moving integration tag only, and do not deploy directly from the moving release pointer.

Typical overlay responsibilities:

- Choose the namespace for one stack instance
- Pin image names and tags with `images`
- Replace the generated config files or patch them with environment-specific values
- Provide Secret material for that stack
- Define the required PVCs, including StorageClasses, access modes, and retention policy suitable
  for durable `q15-workspace` state

Update and rollback guidance:

- Update by resolving a release record, changing all affected digest pins in one deployment commit,
  and rolling the stack.
- Roll back by restoring the previous release's digest tuple while preserving the existing PVCs.
- Do not rotate or drop the contract-required PVCs during normal upgrades or downgrades.
- The checked-in `q15-exec` Deployment bootstraps a fresh `q15-exec-nix` PVC in an init container
  when the mounted volume is missing the image-provided Nix runtime markers. That preserves a
  persistent `/nix` cache without requiring operators to pre-seed the PVC manually. The `q15-exec`
  image also carries an image-local bootstrap copy outside `/nix`, and the process repairs an empty
  `/nix` at startup before it begins serving exec requests.

GHCR runtime images are intended to be publicly pullable without registry auth for ordinary
self-hosted consumption. Maintain the package visibility for these GHCR packages as public in
GitHub's package settings outside this repo.

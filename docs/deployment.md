# Deploying q15

What a release is, what a stack needs before its first start, and the three checked-in deployment
paths: the local development stack, the image-first Compose stack, and the Kubernetes base.

## Release Artifacts

Runtime services are published as OCI images to GHCR on verified pushes to `main`:

- `ghcr.io/q15co/q15-agent`
- `ghcr.io/q15co/q15-exec`
- `ghcr.io/q15co/q15-proxy`
- `ghcr.io/q15co/q15-web`

Published runtime tags:

- `main` tracks the latest successful component build.
- `stable` tracks the latest verified four-image release.
- `YYYY.MM.DD.<run-number>` identifies one immutable release, for example `2026.09.05.164`.

The release job publishes the same DateVer on `q15-agent`, `q15-exec`, `q15-proxy`, and `q15-web`,
verifies all four multi-platform indexes, and then moves each package's `stable` tag to that
release. Unchanged services reuse their existing image index and are retagged without rebuilding.
Deployments pull `stable` only after the publish workflow succeeds. Every verified push to `main`
mints a DateVer, including one that rebuilds nothing, so any commit on `main` can be released.

The build dependency graph is:

- `libs/exec-contract/**` rebuilds `q15-agent` and `q15-exec`.
- `libs/proxy-contract/**` rebuilds `q15-exec` and `q15-proxy`.
- Service implementation and Dockerfile changes rebuild only that service.
- Workspace-level Go dependency changes rebuild all three services.
- Docker build-context policy changes rebuild all three services.

This separates component build identity from stack compatibility: `main` is a moving integration
tag, while a synchronized DateVer identifies one compatible four-image deployment.

GHCR runtime images are intended to be publicly pullable without registry auth for ordinary
self-hosted consumption. Maintain the GitHub package visibility for `q15-agent`, `q15-exec`,
`q15-proxy`, and `q15-web` as public outside this repo.

The `q15` management binary is published as GitHub Release archives on pushed tags that match `v*`,
after the tagged commit passes verification and is confirmed reachable from `main`. The tag is the
version the binary reports, for example `v2026.10.14-d84f422`. The assets are:

- `q15_<version>_linux_amd64.tar.gz`
- `q15_<version>_linux_arm64.tar.gz`
- `checksums.txt`

That same tag names the runtime images. A released CLI renders Quadlet units whose `Image=` lines
pin all four images at the release tag, because a restarted service must not silently change version
under the operator. The release job therefore aliases the immutable images the tagged commit
published under the release tag, and verifies all four before the binary is published; a release
whose images cannot be resolved fails instead of shipping a binary that renders references nobody
can pull. Cut a `v*` tag from a `main` commit whose publish run succeeded, so the images it names
exist. Both halves of a release are then named the same way: `q15 version` reports the tag, and the
rendered units pull the images at it.

## q15

`q15` is the single operator binary: the management verbs, the configuration truth and the
provider-credential verbs. It is Linux only, because it drives a rootless podman stack through the
user's systemd, and it runs on the operator machine, not inside the runtime containers.

Its `auth` verbs generate and inspect `auth.json`:

```bash
q15 auth login --auth-path ./auth.json
q15 auth status --auth-path ./auth.json
q15 auth logout --auth-path ./auth.json
```

For Compose, put the resulting `auth.json` in the directory mounted at `/etc/q15/auth`; do not
single-file bind-mount `auth.json`, because `q15 auth login` and q15-agent write credentials
atomically by replacing the file. For Kubernetes, store it as the `auth.json` key in the agent auth
Secret.

## First Startup Inputs

Before first startup, every long-running q15 stack must provide:

- agent config at `/etc/q15/agent/config.yaml`
- proxy policy at `/etc/q15/proxy/policy.yaml`
- `auth.json` at `/etc/q15/auth/auth.json`
- provider or API secrets required by the chosen agent config
- proxy secret aliases required by the chosen proxy policy
- persistent storage for `/workspace`, `/memory`, `/skills`, `/nix`, `/var/lib/q15/agent`, and
  `/var/lib/q15/proxy`

`/workspace` may begin empty, but it is expected to remain attached to the same stack over time.

## Local Docker Compose Stack (Development Only)

The checked-in local-development stack lives at [docker-compose.yml](../docker-compose.yml) and uses
the templates under [deploy/compose](../deploy/compose). It is not the canonical downstream
deployment-consumer example.

Its interface is organized as:

- [deploy/compose/agent-config.yaml](../deploy/compose/agent-config.yaml) for structured agent
  config
- [deploy/compose/proxy-policy.yaml](../deploy/compose/proxy-policy.yaml) for structured proxy
  policy
- ignored local Docker secret files under [deploy/compose/secrets](../deploy/compose/secrets),
  seeded from tracked `*.example` templates in that same directory
- ignored local OpenAI auth state under [deploy/compose/auth](../deploy/compose/auth), seeded from
  [auth.json.example](../deploy/compose/auth/auth.json.example)

The local-development stack intentionally:

- keeps local source builds enabled with `build:`
- tags those local builds with the canonical GHCR image names on `:main`
- uses a named `q15_workspace` volume for `/workspace`
- uses the root `make compose-*` targets for local iteration

Bring it up with:

```bash
make compose-secrets-init
make compose-up
```

`make compose-up` waits for the stack's health checks. `q15-agent` starts once the proxy, executor,
and Qdrant probes pass and TEI has started; TEI then loads the model (the first start downloads ~1.2
GB of weights), so the first embedding calls may wait on the embedder's retries. The stack runs a
local TEI (Hugging Face Text Embeddings Inference) embeddings backend by default; see
[deploy/compose/README.md](../deploy/compose/README.md) for hardware overrides and the Gemini-to-TEI
migration runbook.

The local-development stack uses:

- default container entrypoints with no runtime flags
- Compose `configs` for non-secret YAML files
- a read-write bind mount for the auth directory at `/etc/q15/auth`
- a named volume shared as `/workspace`
- a named volume shared as `/memory`
- a named volume for `/skills`
- a named `q15_agent_state` volume mounted only into `q15-agent` at `/var/lib/q15/agent`
- a named volume `q15_exec_nix_store` mounted at `/nix` for persistent executor package/store reuse
  across sessions
- Docker secret files under [deploy/compose/secrets](../deploy/compose/secrets)

That `/nix` named volume keeps fetched packages warm across sessions. Docker's named-volume copy-up
behavior also preserves the image's built-in bootstrap Nix runtime on first use, which is why the
local stack can safely keep `/nix` persistent without manually pre-seeding it.

Before using the local-development stack, replace the placeholder values in:

- [deploy/compose/secrets/moonshot_api_key.example](../deploy/compose/secrets/moonshot_api_key.example)
- [deploy/compose/secrets/q15_telegram_token.example](../deploy/compose/secrets/q15_telegram_token.example)
- [deploy/compose/secrets/github_token.example](../deploy/compose/secrets/github_token.example)
- [deploy/compose/auth/auth.json.example](../deploy/compose/auth/auth.json.example)

`make compose-secrets-init` copies those templates to ignored local files without the `.example`
suffix. Edit the local copies, not the templates.

Useful commands:

```bash
make compose-down
make compose-logs SERVICE=q15-agent
make compose-ps
```

## Image-First Compose Deployment Example

The canonical downstream image-first Compose example is
[deploy/compose/docker-compose.image-first.yml](../deploy/compose/docker-compose.image-first.yml).
Supporting notes live in [deploy/compose/README.md](../deploy/compose/README.md).

This deployment-oriented example:

- uses `image:` only, with no `build:`
- accepts one `Q15_IMAGE_TAG` for all q15 services and defaults to `stable`
- mounts persistent named volumes for `/workspace`, `/memory`, `/skills`, `/nix`,
  `/var/lib/q15/agent`, and `/var/lib/q15/proxy`
- mounts `agent-config.yaml`, `proxy-policy.yaml`, and `auth.json` at the exact runtime paths the
  binaries expect
- mounts provider and proxy secret files and resolves them via the `*_FILE` environment pattern
- gates dependent services on proxy, executor, and Qdrant readiness checks

Bring it up with:

```bash
make compose-secrets-init
Q15_IMAGE_TAG=stable docker compose \
  -f deploy/compose/docker-compose.image-first.yml up -d
```

`/workspace` may start empty in this example, but it is expected to persist long-term for one stack.
For long-running deployments, keep that same storage attached across restarts and redeployments. The
`/nix` mount is intentionally persistent and should not be treated as scratch space. A Docker named
volume keeps both the bootstrap Nix runtime and the fetched store paths under `/nix`; an empty bind
mount at `/nix` would hide the image-provided shell and `nix` installation instead of just clearing
the cache.

## Kubernetes Base

A reusable Kustomize base lives under [deploy/kubernetes/base](../deploy/kubernetes/base).

It includes:

- Deployments for `q15-agent`, `q15-exec`, and `q15-proxy`
- Services for `q15-exec` and `q15-proxy`
- ConfigMap generators for agent config and proxy policy examples

The canonical runtime images are:

- `ghcr.io/q15co/q15-agent`
- `ghcr.io/q15co/q15-exec`
- `ghcr.io/q15co/q15-proxy`

The checked-in manifests keep the moving GHCR `:main` tags as placeholders:

- `ghcr.io/q15co/q15-agent:main`
- `ghcr.io/q15co/q15-exec:main`
- `ghcr.io/q15co/q15-proxy:main`

It expects a separate deployment repo or overlay to provide:

- image pinning and rollout policy
- environment-specific namespaces and labels
- Secrets
- PVCs

For long-running deployments, use `stable` for normal updates or replace the placeholders with one
immutable DateVer across all three services. Treat `main` as a moving integration tag only.

The supported Kubernetes model is one namespace per long-running q15 stack. One stack contains:

- one `q15-agent`
- one `q15-exec`
- one `q15-proxy`
- stack-local ConfigMaps and Secrets for agent config, proxy policy, `auth.json`, provider or API
  keys, and runtime tokens
- stack-owned persistent volumes for `/workspace`, `/memory`, `/skills`, `/nix`,
  `/var/lib/q15/agent`, and `/var/lib/q15/proxy`

`q15-workspace` is the durable project and working-state volume for that stack. A newly created
stack may bind an empty `q15-workspace` PVC on first deployment, and that empty initial state still
satisfies the runtime contract. Restarts and redeployments are expected to preserve that PVC so
`/workspace` survives over time.

The checked-in base already reflects that shape with `replicas: 1` for each deployment and
namespace-scoped Services named `q15-exec` and `q15-proxy`. This matches the current exec session
model: `q15-exec` supports multiple concurrent sessions within one pod, but session state is
currently held in memory inside that pod. Keeping one exec pod per stack avoids cross-pod session
routing complexity and keeps storage ownership local to the stack.

The checked-in exec deployment also seeds a fresh `/nix` PVC from the image in an init container
when the mounted volume is missing the bootstrap runtime markers. In addition, the `q15-exec`
process keeps an image-local bootstrap copy outside `/nix` and repairs an empty persisted `/nix` at
startup before serving exec requests. Together those safeguards keep Kubernetes PVC-backed `/nix`
storage persistent without losing the image's built-in shell, profile, cert bundle, or `nix` binary
on first mount.

Validate the base with:

```bash
kubectl kustomize deploy/kubernetes/base
```

The intended workflow is:

1. This repo publishes `q15-agent`, `q15-exec`, and `q15-proxy` images to GHCR.
1. A separate deployment repo pins those images and owns environment-specific overlays.
1. The deployment repo rolls out the updated pod set.

## Self-Hosted Updates And Rollbacks

For Compose and Kubernetes alike:

- Update by pulling the synchronized `stable` tag and rolling the stack.
- Roll back by selecting the previous `YYYY.MM.DD.<run-number>` tag across all three services.
- Preserve the existing persistent storage for `/workspace`, `/memory`, `/skills`, `/nix`,
  `/var/lib/q15/agent`, and `/var/lib/q15/proxy` during normal upgrades and downgrades.

### Memory write ownership

Mount `/memory` writable in `q15-agent` and read-only in `q15-exec`. This is required for cognition
recovery: a shell command or background exec session must not change files reserved by an in-flight
memory run. Use the agent file tools for memory writes; they check ownership and serialize mutations
with recovery. Compose, Kubernetes, and generated Quadlet units enforce this mount contract. Custom
deployments must retain it, and direct host edits must wait until cognition is idle.

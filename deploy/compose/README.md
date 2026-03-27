# q15 Compose Examples

This directory contains the checked-in Compose-facing config, policy, and secret templates for q15.

- [docker-compose.image-first.yml](/deploy/compose/docker-compose.image-first.yml) is the canonical
  downstream deployment example. It uses published `ghcr.io/q15co/q15-*` images only, requires
  `Q15_IMAGE_TAG`, and mounts persistent storage for `/workspace`, `/memory`, `/skills`, `/nix`, and
  `/var/lib/q15/proxy`.
- [docker-compose.yml](/docker-compose.yml) in the repo root is the local-development stack. It
  keeps `build:` enabled and bind-mounts this repo into `/workspace`; it is not the image-first
  deployment example for downstream consumers.
- [agent-config.yaml](/deploy/compose/agent-config.yaml),
  [proxy-policy.yaml](/deploy/compose/proxy-policy.yaml), and
  [secrets/\*.example](/deploy/compose/secrets) are generic templates that downstream repos can copy
  or adapt.

For a long-running image-first deployment:

```bash
make compose-secrets-init
Q15_IMAGE_TAG=sha-<short-sha> docker compose -f deploy/compose/docker-compose.image-first.yml up -d
```

Notes:

- Pin `Q15_IMAGE_TAG` to one immutable published tag across `q15-agent`, `q15-exec`, and
  `q15-proxy`. Do not use `main` as the default for long-running stacks.
- `/workspace` is expected to persist long-term for one stack. It may be empty on first startup.
- Update or rollback by changing the pinned tag and redeploying while preserving the persistent
  volumes.
- GHCR runtime images are intended to be publicly pullable without registry auth for normal
  self-hosted consumption. Maintain the package visibility for these GHCR packages as public outside
  this repo.

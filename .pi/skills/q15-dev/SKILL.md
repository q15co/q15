---
name: q15-dev
description: Develop on the q15co/q15 Go agent platform. Use when editing agent/exec/proxy code, running the repo make workflow, debugging the local Compose stack, or publishing a PR. Covers repo layout, the exec-tool surface, the required make verify gate, and the GitHub publish/merge flow.
---

# q15 development

This repo is the q15 agent platform: an agent (`systems/agent`), an exec sandbox (`systems/exec`),
and a proxy (`systems/proxy`), plus shared contracts under `libs/`.

The public workflow contract is the root `AGENTS.md` and the `Makefile`. Prefer `make` targets over
ad hoc commands.

## Repo layout

- `systems/agent` — the q15 agent binary (`make build-agent` → `bin/q15-agent`).
  - `internal/tools/exec/session_tools.go` — exec tool surface (exec_start/read/write/kill).
  - `internal/cognition/` — cognition, triggers, replay/checkpoint logic.
  - `internal/skills/builtins/` — built-in skills (skill-creator, skill-discovery).
  - `cmd/q15-auth` — bootstrap tool that generates `auth.json` (`make build-auth`).
- `systems/exec` — exec sandbox service (`internal/service/manager.go` is the core).
- `systems/proxy` — proxy service.
- `libs/exec-contract`, `libs/proxy-contract` — shared contracts, tested first by `make test`.
- `scripts/` — repo tooling (`project-setup.sh`, `fmt.sh`, `lint-changed.sh`,
  `go-static-checks.sh`).
- `.tools/` — pinned repo-local toolchain, installed by `make project-setup`.

## Edit loop (when files change)

```bash
make fmt FILES='path/to/file1 path/to/file2'
make lint-changed FILES='path/to/file1 path/to/file2'
```

`make fmt` rewrites in place and does not commit. Restage if formatting changed things.

## Required final validation

`make verify` is the gate. It runs `make project-setup`, `make lint`, then `make test`. Do not
report a task done until it passes; if it fails, fix it or report the exact blocker. For CI-parity
lint on a diff, use `make verify-ci FILES='...'`.

## Local Compose stack

```bash
make compose-secrets-init   # seed ignored local secret files from tracked examples
make compose-up             # build + start q15-agent/q15-exec/q15-proxy
make compose-ps             # health/status
make compose-logs SERVICE=q15-agent   # follow logs (SERVICE=q15-exec|q15-proxy)
make compose-down           # stop + remove orphans
```

Secret/config examples live under `deploy/compose/` (tracked) and are copied to ignored local files
by `compose-secrets-init`.

## Proving runtime behavior

For "did the running stack pick up this fix?" questions, prefer direct runtime evidence over
log-only reasoning, in this order:

1. `make compose-ps` — confirm the relevant services are up; stop if they are not.
1. Inspect mounted runtime artifacts directly (for cognition changes, the state and checkpoint files
   under `/memory/cognition/*`) for fresh records matching the patch.
1. Search logs for concrete failure strings (`context_length_exceeded`, panic text) only to explain
   anomalies the artifacts surface.
1. For background jobs, poll once more before concluding — a stale checkpoint may mean work is still
   in flight.

## GitHub publish/merge flow

1. Refresh from trunk: `git fetch origin main`, then rebase onto `origin/main`.
1. Confirm `gh auth status` and `git status -sb`.
1. `git push -u origin <branch>`.
1. Open a non-draft PR (or set existing draft ready) with a clear title + body.
1. Watch checks: `gh pr checks <number> --watch --interval 30`.
1. Merge only when green, using the mode the user named (e.g. squash). Ask before merging unless
   told to.

If git sync stalls on signing/pinentry, switch fetch/push to HTTPS and disable commit signing
locally for the blocked rebase, then restore it.

# The operator CLI lives in the agent module, and imports no container bindings

Status: accepted

`q15` is built from `systems/agent/cmd/q15`, with its packages under `systems/agent/internal/`, so
that the CLI can construct the agent's real `config.Config`, validate it and render it. A separate
module would need a second description of the same schema, and the second one drifts. The cost of
folding is dependency weight, so the constraint targets the weight instead of the location: no
package under `systems/agent` may import `github.com/containers/*`, enforced by
`scripts/check-no-containers-import.sh` against both the import graph and the source text.

The weight was measured before the choice, in a scratch module importing only
`github.com/containers/podman/v5/pkg/bindings/containers@v5.8.8` on Go 1.26.7: 384 modules, 764
linked packages, and a 33,158,828-byte `CGO_ENABLED=0` binary against a 3-module, 5,845,354-byte
baseline. It also would not build without `-tags containers_image_openpgp`, a tag this repository
uses nowhere.

## Considered options

- A separate CLI module. Rejected: it needs either a parallel configuration template or a premature
  extraction of the schema into `libs/`, and the measurement indicts an import rather than a
  location.
- Folding the CLI in with the bindings allowed. Rejected: `systems/agent` is the security-critical
  component that runs `govulncheck` in CI, and 384 modules for a CLI-only reason is not a trade
  worth making.

## Consequences

- The stack verbs reach podman and systemd by running them, never through a socket API. That is also
  what keeps [ADR 0005](0005-systemd-owns-the-stack-lifecycle.md) as thin as it is.
- When a verb genuinely needs a write that systemd cannot express, its client goes into a module of
  its own, so the build tag and cgo decision is quarantined there rather than spread through the
  agent.

Evidence: `scripts/check-no-containers-import.sh`, [#215](https://github.com/q15co/q15/issues/215),
[#217](https://github.com/q15co/q15/issues/217), [#260](https://github.com/q15co/q15/pull/260).

# Architecture decision records

One file per decision that is hard to reverse, surprising without context, and the result of a real
trade-off. If a decision is easy to reverse, or nobody would wonder why it went the way it did, it
does not belong here; the issue that made it is enough.

A record is written from repository evidence and cites the issue or pull request it came from,
because a reader of the repository cannot see a conversation. Where a record states a measurement it
states the method, so the next person can reproduce or refute it.

## The records

| Record                                             | Decision                                                                     |
| -------------------------------------------------- | ---------------------------------------------------------------------------- |
| [0001](0001-tool-credentials-injected-by-proxy.md) | Tool and shell credentials are injected by the proxy, not held by the agent  |
| [0002](0002-agent-state-is-files.md)               | Agent state is files on a volume, not a service                              |
| [0003](0003-cli-lives-in-the-agent-module.md)      | The operator CLI lives in the agent module and imports no container bindings |
| [0004](0004-mit-licence.md)                        | MIT, with a DCO and no CLA                                                   |
| [0005](0005-systemd-owns-the-stack-lifecycle.md)   | systemd and Quadlet own the stack lifecycle; podman is read, never written   |

[0000-template.md](0000-template.md) is the template. That number is reserved for it and is never a
decision.

## Writing one

Copy the template to the next free number with a short slug, for example
`0006-transcript-schema-portability.md`. Then:

1. Set the status to one of `proposed`, `accepted`, `deprecated`, or `superseded by ADR-NNNN`.
1. Cite the issue or pull request. A decision with no provenance is an assertion.
1. Never edit a record to change its decision. Write a new record that supersedes it, and update the
   old one's status to point at the new one, because the reasoning that turned out to be wrong is
   worth keeping.

## Decisions that are settled and not recorded yet

Each of these currently lives only in an issue, a pull request, or a discussion, which is what makes
them hard to find. They are the next work.

| Decision                                                                                 | Where it lives today                                                                             |
| ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| The runtime is four services, one process each, and why they are split that way          | [#215](https://github.com/q15co/q15/issues/215), `docs/architecture.md`                          |
| Only `q15-web` publishes a port, and only on `127.0.0.1`                                 | `scripts/compose-contract-test.sh`, `deploy/compose/`                                            |
| Tool and shell credentials live in `systemd-creds` behind podman's shell driver          | [#215](https://github.com/q15co/q15/issues/215)                                                  |
| Synced, backup-eligible passkeys are accepted, and per-device revocation is the cost     | [#257](https://github.com/q15co/q15/issues/257)                                                  |
| Model sources are values of the deployment profile, and local-first is the default       | [#273](https://github.com/q15co/q15/issues/273), [#221](https://github.com/q15co/q15/issues/221) |
| The proxy policy schema is a shared contract under `libs/proxy-contract`                 | [#271](https://github.com/q15co/q15/issues/271), [#272](https://github.com/q15co/q15/pull/272)   |
| Working memory is capped, and demoted rather than truncated when it is over              | [#258](https://github.com/q15co/q15/issues/258), [#259](https://github.com/q15co/q15/pull/259)   |
| The site's install page is the source of truth for install instructions, not this README | [#253](https://github.com/q15co/q15/pull/253)                                                    |

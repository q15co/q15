# Agent state is files on a volume, not a service

Status: accepted

The things worth keeping are directories the operator owns. `/workspace` is the project tree,
`/memory` is the transcript and what was extracted from it, and `/skills` is the capability library.
Nothing sits in front of them, so a backup is a copy, a restore is a copy back, and there is no
export path to write and no migration service to keep running. A store behind a service would buy
query power and cost custody, and custody is the point: the operator has to be able to read the
state without the runtime.

## Consequences

- The transcript is schema-versioned and upgraded in place at startup, so an upgrade writes to the
  data rather than to a server. Unreadable turn files are moved to `/memory/history/quarantine/`
  instead of failing the boot.
- Scheduled-job state is deliberately outside the memory tree, under `/var/lib/q15/agent/schedule/`,
  so a backup of `/memory` alone is incomplete. The volume list in `deploy/compose/` is the
  authoritative set, not this record.
- A layout change is a breaking change for anyone who copied these directories, which is why the
  layout is documented as a contract rather than as an implementation detail.

Evidence: [docs/architecture.md](../architecture.md), [docs/configuration.md](../configuration.md),
`deploy/compose/`.

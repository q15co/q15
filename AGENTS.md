# AGENTS

This repository uses `make` as the public workflow contract. Agents should use the repo-managed
tooling and targets here instead of ad hoc commands.

## Environment

- Optional on NixOS or other Nix setups:

  ```bash
  nix develop
  ```

- Required before any linting, formatting, or tests:

  ```bash
  make project-setup
  ```

`make project-setup` installs the pinned repo-local toolchain under `./.tools`. Agents should rely
on those tools, not global installs.

## Edit Loop

When files change, use the shared repo commands in this order:

1. Format the files you changed.

   ```bash
   make fmt FILES='path/to/file1 path/to/file2'
   ```

1. Run fast changed-file checks on the same file set.

   ```bash
   make lint-changed FILES='path/to/file1 path/to/file2'
   ```

1. Restage files if formatting changed them.

`make fmt` rewrites files in place. It does not commit anything.

## Required Final Validation

Before claiming the task is complete, agents must run:

```bash
make verify
```

`make verify` is the required final gate. It runs:

- `make project-setup`
- `make lint`
- `make test`

If `make verify` fails, do not report the task as done. Fix the failure or report the exact blocker.

## CI Parity

CI uses the same repo workflow. The CI-specific lint path is:

```bash
make verify-ci FILES='path/to/file1 path/to/file2'
```

Use `make verify-ci` when you specifically need to mirror the diff-aware CI lint/static-analysis
path. Use `make verify` for final local validation.

## Expectations

- Prefer `make` targets over calling individual linters directly.
- Prefer `make fmt FILES=...` and `make lint-changed FILES=...` during iteration.
- Always finish with `make verify` unless the user explicitly says not to or an external blocker
  prevents it.
- In the final handoff, state which validation commands were run and whether they passed.

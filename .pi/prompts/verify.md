---
description: Run make fmt/lint-changed on changed files, then make verify, and report results
---

# Verify

Run the project's validation gate end-to-end and report results. Do not claim the task is done until
it passes.

1. If any files changed, format and check them first:
   - `make fmt FILES='<changed files>'`
   - `make lint-changed FILES='<changed files>'`
1. Run the full gate: `make verify` (this runs `make project-setup`, `make lint`, and `make test`).

Report exactly which commands passed or failed. If `make verify` fails, fix the failure or state the
exact blocker. For CI-parity lint on just the diff, use `make verify-ci FILES='...'`.

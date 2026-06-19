---
description: Publish the current branch end-to-end (push, PR, watch checks, ask before merge)
---

# PR ready

Publish the current branch end-to-end.

1. Work from latest trunk: `git fetch origin main` and rebase onto `origin/main`.
1. Confirm `gh auth status` and `git status -sb`.
1. `git push -u origin HEAD`.
1. Open a non-draft PR (or set an existing draft ready for review) with a clear title and a body
   summarizing the change.
1. Watch checks: `gh pr checks <number> --watch --interval 30`.
1. When green, ask before merging. If I asked for a merge, use the mode I named (for example
   squash).

If git sync stalls on signing or pinentry, switch fetch/push to HTTPS and disable commit signing
locally for the blocked rebase, then restore it.

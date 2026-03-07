---
name: skill-discovery
description: Discover useful skills from local and public skill ecosystems, then adapt them into q15-native skills. Use when the agent needs to find an existing skill for a task, learn from skills.sh or public skill repos, or avoid reinventing a workflow from scratch.
---

# Skill Discovery

Find promising skills from local and public sources, then adapt what you learn into q15-native
skills.

## Goals

- Find skills that already solve the problem or overlap heavily.
- Learn from the broader agent-skills ecosystem, not just the local `/skills` tree.
- Reuse good patterns for naming, descriptions, structure, and supporting files.
- Adapt ideas without copying weak instructions, irrelevant baggage, or bad layouts.

## Workflow

1. Clarify the target capability: what problem should the skill solve, and what kind of workflow or
   knowledge should it provide?
1. Start with the available-skills catalog in the system prompt and check local skills first.
1. Read only the most relevant local `SKILL.md` files from `/skills/@builtin/...` and
   `/skills/<name>/...`.
1. If local skills are insufficient, search public sources such as `skills.sh` and public
   agent-skill repositories. Use `web_search` when available, or fetch known pages and repo files
   directly with `web_fetch`.
1. When you find a promising public skill, read the source `SKILL.md` first. Inspect `scripts/`,
   `references/`, or `assets/` only when they appear relevant.
1. Compare:
   - scope and trigger description
   - assumptions about the target agent and toolset
   - file layout and colocation of resources
   - whether scripts or references are actually earning their keep
1. Learn from the candidate skill, then rewrite it as a q15-native local skill. Do not clone another
   skill verbatim.
1. Adapt the result for q15:
   - write the final skill under `/skills/<name>/...`
   - keep final scripts, references, and assets colocated with the skill
   - remove instructions that assume another agent's tools, prompts, slash commands, or UI
   - prefer q15's existing file tools, `validate_skill`, and nix-oriented execution model
1. If a candidate skill is low quality, too broad, or mismatched, note that and design the new skill
   cleanly instead of inheriting its problems.
1. When you are ready to create or update the local skill itself, switch to
   `/skills/@builtin/skill-creator/SKILL.md`.

## Public sources

- `skills.sh` for broad discovery and popular skills
- public agent-skill repositories such as `anthropics/skills`, `vercel-labs/agent-skills`, and
  similar collections
- any public repository that contains a clearly structured `SKILL.md` and supporting files worth
  adapting

## Heuristics

- Prefer a few high-signal examples over reading every skill in the tree or every public repo.
- Check whether an existing local skill should be extended instead of creating a near-duplicate.
- Prefer sources with clear structure, good trigger descriptions, and supporting files that match
  the claimed workflow.
- Favor concise descriptions that explain user intent and trigger conditions.
- Final skill resources belong under the skill directory, not scattered through `/workspace`.
- Treat public skills as source material to learn from, not artifacts to mirror exactly.

## Output

When discovery materially influenced the result, briefly note:

- which local or public skills you inspected
- which patterns you kept
- which patterns you rejected and why

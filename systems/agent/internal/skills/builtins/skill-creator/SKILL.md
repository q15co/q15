---
name: skill-creator
description: Create or update q15 skills stored under /skills/<name>. Use when the agent needs to make a new skill, revise an existing skill, improve SKILL.md metadata, or organize scripts, references, and assets for a reusable workflow.
---

# Skill Creator

Create or update q15 skills directly in `/skills/<name>/`.

## Required structure

- Every skill lives in its own directory.
- Every skill must contain `SKILL.md`.
- Optional supporting directories: `scripts/`, `references/`, `assets/`.
- Keep every final skill-owned file inside that skill directory. For example, put helper scripts in
  `/skills/<name>/scripts/...`, references in `/skills/<name>/references/...`, and assets in
  `/skills/<name>/assets/...`.
- Do not leave final skill resources in `/workspace`. Use `/workspace` only for temporary scratch
  work, then move the final files into `/skills/<name>/...`.

## Frontmatter

- Include YAML frontmatter with:
  - `name`: lowercase letters, digits, and hyphens only; match the directory name.
  - `description`: explain what the skill does and when to use it.
- Keep descriptions concise and focused on user intent, not implementation trivia.

## Workflow

1. Before creating a new skill from scratch, inspect a few relevant existing skills for patterns you
   can reuse. Use `/skills/@builtin/skill-discovery/SKILL.md` when you need help finding nearby
   examples, checking whether a similar skill already exists, or learning from public skill
   ecosystems like `skills.sh` and public skill repos.
1. Decide the skill name and the trigger description.
1. Adapt useful patterns, but do not blindly copy another skill's wording, structure, or mistakes.
   Rewrite the skill so it is specific to the new workflow.
1. Create or update `/skills/<name>/SKILL.md`.
1. When creating skill files, use absolute paths under `/skills/<name>/...` so scripts, references,
   and assets are colocated with the skill instead of landing in `/workspace`.
1. Add `scripts/`, `references/`, and `assets/` only when they materially help repeated use.
1. Keep `SKILL.md` focused on workflow and decisions; move bulky detail into `references/`.
1. Before finishing, make sure every final skill resource is stored under `/skills/<name>/...` and
   not left behind elsewhere.
1. Call `validate_skill` on the skill directory before considering the work complete.

## Guidance

- Prefer small, focused skills over large catch-all skills.
- Use progressive disclosure: keep the main skill concise and load deeper references only when
  needed.
- Look at a small number of relevant existing skills before authoring. Reuse patterns that are
  clearly good fits, but do not cargo-cult questionable instructions or duplicate a skill that
  already covers the task.
- Avoid extra top-level docs like README files unless the user explicitly asks for them.
- When editing an existing skill, preserve useful resources and tighten the description before
  adding more instructions.
- If you create a helper script or reference file during development, move it into the skill
  directory before you stop. A skill is not complete if its supporting files still live outside
  `/skills/<name>/...`.

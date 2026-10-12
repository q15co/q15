# q15 architecture

How the runtime is put together: the services and what each one owns, how a conversation and its
memory are stored on disk, and how the agent keeps local time and reports progress back to a chat.

## Architecture

- The agent owns prompt assembly, tool wiring, Telegram I/O, memory management, scheduled-job state,
  and rooted file operations.
- `q15-exec` owns command execution and reports the authoritative model-visible runtime directories:
  `/workspace`, `/memory`, and `/skills`.
- `q15-proxy` owns policy, egress, and auth-env injection for exec sessions.
- `q15` is the operator binary: it drives the stack and produces `auth.json` outside the runtime
  containers.

This maps directly onto the intended deployment model: one `q15-agent`, one `q15-exec`, and one
`q15-proxy` running together in Compose or Kubernetes.

In Kubernetes, the supported topology is one namespace per q15 stack. Each stack contains one
`q15-agent`, one `q15-exec`, one `q15-proxy`, stack-local ConfigMaps and Secrets, and stack-owned
persistent volumes for `/workspace`, `/memory`, `/skills`, `/nix`, `/var/lib/q15/agent`, and
`/var/lib/q15/proxy`. The namespace is the isolation boundary for that stack. Downstream multi-stack
deployments should repeat this stack in separate namespaces.

`/workspace` is not ephemeral scratch space. For each stack it is stack-owned, persistent, and
long-lived state that carries the durable project tree and working files over time. A newly created
stack may attach an empty persistent volume or empty host directory at `/workspace`; that empty
initial state is valid, and operators may populate it later through normal agent work or manual
setup.

## Conversation History

- Completed turns are stored as JSON files under `/memory/history/turns/YYYY/MM/DD/`.
- Transcript sequence metadata is stored under `/memory/history/state/head.json`.
- The current persisted format is `schema_version: 5` with canonical ordered `messages[].parts[]`
  entries of type `text`, `reasoning`, `tool_call`, and `tool_result`.
- On agent startup, existing history is eagerly upgraded in place before replay. Unreadable turn
  files are moved to `/memory/history/quarantine/`.
- Provider-specific opaque replay state is only persisted on `reasoning.replay`. Cross-provider
  fallback uses portable transcript data first.
- When a provider exposes only opaque reasoning for an assistant tool-call replay, q15 backfills an
  explicit portable reasoning placeholder before persisting the turn so later fallback remains
  replayable.
- After a scheduled notification is transport-acknowledged, its exact text is idempotently appended
  as an assistant event with structured job, run, transport, and delivery-time metadata. During
  replay, q15 exposes that provenance in a trusted system-role record immediately before the exact
  delivered assistant text; assistant-authored lookalike metadata is never trusted. The scheduled
  job's private prompt/tool trace is not included.

## Memory Layout

- `/memory/core/` stores the agent's always-injected identity, personality, and self-model files.
- `/memory/semantic/` stores durable extracted knowledge the agent knows in the canonical editable
  files `facts.md`, `preferences.md`, and `projects.md`; it is not auto-injected.
- `/memory/working/WORKING_MEMORY.md` is the canonical prompt-visible working-memory artifact for
  bounded active state.
- Other files under `/memory/working/` are not implicitly prompt-visible, and `/memory/notes/`
  remains the general notebook area.
- `/memory/history/` stores the canonical episodic transcript and its sequencing metadata.
- `/memory/cognition/` is reserved for system-owned control, indexing, and background-maintenance
  state.
- `/memory/notes/inbox/`, `/memory/notes/zettel/`, and `/memory/notes/maps/` form the auxiliary
  zettelkasten notebook layer.
- Scheduled-job operational state is deliberately outside Git-backed memory:
  `/var/lib/q15/agent/schedule/jobs/` stores active definitions and
  `/var/lib/q15/agent/schedule/runs/YYYY/MM/DD/` stores completed run records.

## Prompt-Visible Temporal Metadata

Interactive user turns may carry runtime-injected temporal metadata at the start of the
prompt-visible user message:

```text
<message_meta day_of_week_local="Wednesday" timestamp_local="20260401T184700+0200" since_prev_user_message="3m42s"/>
```

- The tag describes that same user message only; it is not user-authored content.
- `day_of_week_local` makes the local calendar context explicit without asking the model to derive
  the weekday from the timestamp.
- `timestamp_local` uses the runtime host's local timezone with a numeric offset and carries the
  full local date and time.
- When user-facing time references are tied to the conversation, the agent should prefer that same
  runtime local timezone over UTC unless the user explicitly asks for another timezone.
- Explicit field names are used intentionally instead of abbreviations like `ts` or `gap` so the
  model reads the timing semantics without guesswork.
- The shipped Compose and Kubernetes manifests set container `TZ`, and q15 uses the container's
  local timezone for prompt-visible timestamps.
- The shipped `q15-agent` and `q15-exec` images also include timezone data so shell tools can
  resolve named zones such as `Europe/Berlin` correctly.

## Telegram Replies And Progress

In private chats, Ollama and OpenAI-compatible providers can show a growing reply draft while the
model generates. Updates reuse one draft per run, are coalesced to at most one per second, and
refresh every 20 seconds during longer waits. Completion sends the final reply through the normal
safe rich message path. Telegram's Stop button cancels the current run and discards its unfinished
draft; the worker remains available for the next message.

While waiting for a model, the draft uses Telegram's native thinking block. When Ollama or an
OpenAI-compatible provider streams reasoning text, that block shows a recent excerpt, limited to 640
characters in progress mode or 1,600 in verbose mode, and at most eight lines. Answer text replaces
the thinking block as it arrives. Thinking is ephemeral and never added to the final reply.
Providers without reasoning streaming still get the native thinking indicator in supported chats.

Use `/progress quiet`, `/progress progress`, or `/progress verbose` to choose the amount of
feedback:

- `quiet` keeps thinking, partial replies, and routine tool activity hidden, with the existing
  long-wait notice.
- `progress` shows the current action with a short command, file, or search preview.
- `verbose` allows a longer preview of that same action.

Commands appear in Bash code blocks, preserving line breaks and shell syntax. Progress mode shows up
to 320 characters and five lines; verbose mode allows 640 characters and ten lines. File and search
previews stay on one line. Tool output is not dumped into progress messages. Tool progress shares
the active draft, so the chat does not accumulate a message for every tool call. Unsupported chats,
oversized previews, and draft API errors retain the ordinary progress and final-message path. Draft
answers use the same untrusted Markdown rendering boundary as final text; only the Telegram adapter
can create a thinking block.

Internally, `agent.StreamingModelClient` is an optional extension of `ModelClient`. Providers send
ordered content-only callbacks. The separate optional `agent.ReasoningStreamingModelClient`
extension provides reasoning text through `model_reasoning_delta` events. Opaque replay and tool
arguments remain in the canonical result. The engine emits synchronous content and reasoning events
between model-turn and tool/run lifecycle events. Synchronous delivery supplies backpressure without
an event queue; observers own bounded buffering and display throttling. Each `model_turn_started`
begins a fresh attempt, allowing consumers to replace a failed model's partial preview during
fallback. Runs without an observer keep using `Complete`. Incomplete provider streams return errors
and cannot become final transcript text.

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

The working files `WORKING_MEMORY.md`, `OPEN_LOOPS.md`, and `LEDGER.md` are maintained only by
cognition jobs. Foreground and scheduled runs can read them but cannot mutate them, even while
cognition is idle; this keeps every exit under the jobs' ledger validation. Other memory file
mutations use the agent's `write_file`, `edit_file`, and `apply_patch` tools. A cognition run
reserves its snapshotted paths until validation or recovery completes. File tools hold the memory
repository lease during the actual mutation; another run cannot write a reserved path, including
through a directory alias. Reads and unrelated memory paths remain available. The execution service
mounts `/memory` read-only, so shell commands and background sessions cannot bypass this ownership
boundary. Other deployment arrangements must preserve that mount contract. Direct host edits are an
operator action and must wait until cognition is idle.

## Semantic Claim Src Citations

Every durable claim carries a `src` field in its `{...}` metadata line that names the evidence
behind the statement. One grammar owns what a `src` may cite; the conversion apply step
(`semantic_memory.extract`) validates every claim in the files its run wrote against it:

- `t<seq>` is a transcript sequence marker: the letter `t` followed by a positive decimal integer
  with no leading zero, no ranges and no punctuation, for example `t4312`.
- `mem:<path>:<byte-offset>` is a memory-file citation: `<path>` sits under `semantic/` or
  `working/`, and `<byte-offset>` is decimal. The memory-repair flow uses this form for a claim it
  adopted out of a drained memory file.

Several citations are separated by commas. Anything else - guessed sources, URLs, dates, `t0`,
citation ranges, trailing punctuation - fails the claim src grammar. The grammar itself is not
relaxed at the boundary; its refusals stay exact.

The apply boundary treats one src-grammar refusal differently from every other validation failure. A
pass that introduces a claim whose citations alone are invalid does not lose its valid claims: the
apply step defers that one claim - it removes it from the written file, names the claim ID and the
refusal in the run result's `deferred_claims` metadata (bounded list; the overflow is disclosed as a
count), and the run still applies its valid claims and still advances the extraction checkpoint.

One more disclosed deferral class extends the same contract: a claim the pass wrote so badly it
never gained a stable ID and statement. The apply step drops that raw item, names it by the byte
offset of its head line in the written file under the same `deferred_claims` metadata, and keeps the
rest of the pass; an item that names no claim at all - a bare bullet or index-shaped prose - still
rejects the run whole, as does a parse-invalid rewrite of a claim the baseline already knows.

The protective contract is unchanged: a rewrite that drops or replaces the provenance of an existing
durable claim rejects the whole run and rolls it back, because silently rewriting durable knowledge
is never acceptable.

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

## Model Providers And Roles

The model boundary has three layers. Kind is the connection family in `providertypes`, the only
provider enum; a task does not become a kind. A model is a provider-bound catalog row with
normalized capabilities and per-capability provenance. Interactive and decision selections are
runtime state persisted under `/workspace/.q15/agent/selection.json` and switched with tools.
Embedding model and transport settings remain owned by the embedding tool until #376; no chat or
decision selection change can replace the model used for its stored vectors.

Incoming provider and models.dev `type: decision` metadata normalizes into `Capabilities.Decision`.
Operator `override` evidence wins over provider API or artifact `derived` evidence, which wins over
models.dev `declared` evidence. Enrichment fetches `api.json?type=all` and enriches only live
models. The boolean capability struct and loose flags remain until #375. `list_models` filters
effective capabilities and reports their provenance.

Boot restores a selection when it is roster-valid with the operation's effective capability;
otherwise it selects an eligible live model and persists it. Interactive selection prefers text plus
tool calling. Chat and decision dispatch repeat their respective capability checks. A model without
chat support cannot enter chat dispatch even if it accepts text input. If a model genuinely supports
both operations, its identity does not prevent either. An absent decision candidate leaves
`decision_check` unavailable while existing chat and cognition jobs continue operating.

Decision calls use `decision.Client` at `decision/v1`. In-tree Go adapters are checked by the
compiler; the version declaration reserves a check for any future adapters loaded at runtime, with
no plugin loader introduced here. The application resolves only the held decision selection,
constructs its provider adapter, and injects that port into `decision_check`. The tool owns neither
selection nor HTTP construction. Clients are keyed by provider connection facts so reloads cannot
reuse an obsolete connection.

The cognition runner still uses the shared chat/tool engine: per-job persisted model overrides win,
otherwise it inherits the interactive model. The provider foundation in PR 374 supports the
[typed memory cutovers](https://github.com/q15co/q15/issues/367) and subsequent
[decision integration](https://github.com/q15co/q15/issues/373). System One answers fixed choices,
probabilities, and scores; it cannot generate arbitrary claim text or a complete repair JSON object.
The cognition integration must retain a generative model for new content and keep validation,
journaling, checkpoint accounting, and rollback in Go.

One Ollama client implements the chat, streaming, reasoning-streaming, and decision ports. Its
connection value builds the normalized root, trimmed key, bearer transport, HTTP client, and SDK
client once. The decision method uses `SystemOne` from the Ollama Go SDK (`v0.40.6`), with a
120-second call-context deadline; the shared HTTP client imposes no timeout on chat streams.
OpenAI-compatible providers with decision models and dedicated `systemone-compatible` providers use
the shared System One HTTP adapter. API roots belong to the provider adapter: Ollama's SDK adds
`/v1/systemone` to its native root; compatible providers default to `systemone` relative to their
documented API root. Provider `decision_path` overrides either path: relative paths extend the root,
and leading slashes replace the path at the same origin. Discovery remains rooted independently at
`models`. Both adapters preserve the typed request/response validation and option order.

Ollama Cloud API keys are supplied by the SDK adapter's transport. The SDK otherwise attempts
SSH-key authentication for `ollama.com` before calling the transport, so a bearer-authenticated SDK
client uses an internal authority that the transport replaces with the configured endpoint. This
keeps API-key access independent of Ollama's local sign-in files. Non-success responses are rejected
before SDK decoding, and SDK errors are sanitized to avoid exposing response content.

Upstream contracts: [models.dev catalog filtering](https://github.com/anomalyco/models.dev#api),
[Ollama decision API](https://docs.ollama.com/capabilities/decision), and
[TypeSafe model discovery](https://docs.typesafe.ai/models#listing-models).

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

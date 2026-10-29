# q15 architecture

How the runtime is put together: the services and what each one owns, how a conversation and its
memory are stored in Postgres and authored files, and how the agent keeps local time and reports
progress back to a chat.

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

Postgres is the sole conversation store. `memory.turns` assigns numeric identities; messages and
ordered parts have their own keys and foreign keys. Separate typed tables store text, reasoning,
tool calls/results, attachments, original user clocks and acknowledged scheduled deliveries.
Completed source turns are immutable; deleting a turn cascades to its messages, chunks, digest and
vectors. Relationships between a digest and its earlier context are recorded explicitly.

The schema uses relational columns for identities, order, timestamps and relationships. JSONB is
reserved for valid tool arguments and opaque provider replay structures. Exceptional payloads that
Postgres text or JSONB cannot represent, including NUL characters and malformed legacy arguments,
use mutually exclusive bytea columns. Original source content is retained. pgvector stores typed,
dimension-checked vectors; native Postgres full-text indexes provide lexical search. Existing AGE
and other application schemas are not altered.

A background indexer creates one short digest per completed turn and chunks original message and
tool text. Digests use the configured Ollama model, with no interactive-model fallback. Each digest
records its model, prompt version, input hash, actual token usage and earlier source dependencies.
Failures retry independently per turn. Source persistence does not wait for generation or indexing.
There is no cognition controller, verification review, semantic extraction, consolidation, repair
protocol, open-loops file or conversion ledger.

Context assembly combines recent source turns, dated summaries of the intervening history, and
relevant retrieved summaries or original-message excerpts. Every block identifies its source and
whether it is generated. Recent turns are excluded from retrieval, and overlapping gap/retrieval
results are deduplicated. Short acknowledgements use nearby conversation context for retrieval.
`history_search` finds older evidence and `history_read` pages original turn JSON.

At the start of each user turn, the runtime selects history within a configurable assembly target,
counting instructions, tool definitions and user input. It shortens copied historical tool-output
and reasoning excerpts, represents older turns with stored summaries, and removes optional history
as needed. The first actual model attempt fixes that selection for the run.

Subsequent model/tool steps can grow into the selected model's full context window, with output and
safety reserves. Each provider attempt, including model fallback, checks that window and any
explicit encoded-byte cap. Current-turn evidence stays intact while it fits; further history
trimming and evidence shortening occur only under actual capacity pressure. Saved user clocks and
complete tool pairs remain intact, and an irreducible request fails before dispatch. Runtime
counters describe the usable window rather than the assembly target. Token counts are conservative
estimates with a margin, rather than exact provider counts. Stored originals are unchanged; there is
no active-run summarization or cloud recall benchmark in this slice.

Library and document search retain their existing Qdrant collections and embedding bindings.
Conversation embeddings have a separate explicit space: provider, model, dimensions and prompt
prefix version. EmbeddingGemma 2 uses its documented query/document prefixes and native Ollama
requests disable silent input truncation. Query embedding failure degrades hybrid search to lexical
search; background work remains resumable.

## Authored Files

`/memory/core/*.md` remains the authored identity and guidance injected into each prompt. Skills and
ordinary notebook files remain authored files. The runtime initializes only identity and notes; it
never seeds the retired memory layers. Old files are migration/archive material and are not runtime
state. File tools serialize authored changes with the repository lease. The executor mounts that
filesystem read-only.

Scheduled-job definitions and run records remain under `/var/lib/q15/agent/schedule/`. Delivered
notifications enter Postgres only after transport acknowledgement, with structured provenance and
idempotent delivery identity. Their private prompts and tool traces are not copied into chat
history.

See [the cutover runbook](postgres-history-cutover.md) for import, verification and preparation.

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
`decision_check` unavailable while chat continues operating.

Decision calls use `decision.Client` at `decision/v1`. In-tree Go adapters are checked by the
compiler; the version declaration reserves a check for any future adapters loaded at runtime, with
no plugin loader introduced here. The application resolves only the held decision selection,
constructs its provider adapter, and injects that port into `decision_check`. The tool owns neither
selection nor HTTP construction. Clients are keyed by provider connection facts so reloads cannot
reuse an obsolete connection.

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

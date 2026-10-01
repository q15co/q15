# q15-web

The web tier serves the browser socket, transcript API and app from one origin. It dials the agent's
Unix bridge without an agent credential, never mounts memory, and stores no transcript or auth
state. Only connection handles and active progress exist in memory. The temporary gate will be
replaced by the authentication slice through `gate.Authorizer.RequireScope` and
`gate.WithPrincipal`.

## Build and run

```bash
make project-setup
make build-web
Q15_WEB_ORIGIN=http://localhost:8080 Q15_WEB_TOKEN=local-development \
  Q15_WEB_BRIDGE=unix:///run/q15/bridge.sock ./bin/q15-web
```

The agent must have `agent.bridge.listen_target: unix:///run/q15/bridge.sock` and provision that
socket. The web process needs permission to connect to it. Startup checks the bridge within five
seconds and fails on any protocol mismatch. A supervisor can restart it while the agent starts up;
startup never serves requests with an unchecked bridge. Active stream interruptions retry after one
second with the last received **event index**. An agent restart invalidates the ephemeral session,
sends `resync_from_head`, and the next send allocates a fresh handle.

| Variable                              | Default                       | Purpose                                       |
| ------------------------------------- | ----------------------------- | --------------------------------------------- |
| `Q15_WEB_LISTEN`                      | `127.0.0.1:8080`              | HTTP listen address                           |
| `Q15_WEB_BRIDGE`                      | `unix:///run/q15/bridge.sock` | Credential-free bridge socket                 |
| `Q15_WEB_ORIGIN`                      | Required                      | Exact public origin, without a trailing slash |
| `Q15_WEB_TOKEN`                       | Required                      | Temporary owner token                         |
| `Q15_WEB_DIR`                         | Embedded assets               | Confined development bundle directory         |
| `Q15_WEB_TLS_CERT`, `Q15_WEB_TLS_KEY` | Unset                         | Optional direct TLS; both must be supplied    |

HTTP on the loopback listener supports local development or an ingress that terminates TLS. Use the
TLS pair for direct HTTPS. The temporary gate accepts `Authorization: Bearer <token>` for tooling or
browser Basic authentication with username `q15` and the token as password. Tokens in query strings,
cookies and socket subprotocols are ignored. A browser's native Basic challenge lets the app and
socket share credentials without storing the token in JavaScript.

All paths except the exact `/healthz` require the `chat` scope. The header policy covers errors,
assets and upgrades. Socket upgrades require both the exact configured `Origin` and
`Sec-Fetch-Site: same-origin`; request Host and forwarded headers do not redefine the allowed
origin.

## Browser contract

The browser contract is version 1, independent of the gRPC protocol version. Every frame is
`{v, id, type, ts, seq, payload}`. `v` is an integer; `ts` is an RFC3339 UTC timestamp. All int64
sequence and cursor values are **decimal JSON strings**, preserving numbers above JavaScript's safe
integer limit. Clients should compare them as `BigInt` values.

| Direction | Types                                                                                            |
| --------- | ------------------------------------------------------------------------------------------------ |
| Client    | `hello`, `sync`, `msg.send`, `msg.abort`, `msg.ack`, `msg.status`, `presence`, `ping`            |
| Server    | `ready`, `turn.start`, `delta`, `snapshot`, `msg.final`, `msg.status`, `notice`, `pong`, `error` |

The first frame must be `hello` with `{cursor:"0"}` or the last fully consumed transcript sequence.
Each request supplies a nonempty `id` of at most 128 bytes. `sync` requests the same replay later.
Replay emits complete canonical messages oldest first, followed by
`ready{head_seq,cursor,device_id}`. `ready.cursor` is the newest readable record, while `head_seq`
may include a still unwritten live turn. Persist the completed replay cursor after consuming replay;
never advance it merely from the allocated head. More than 500 missed readable turns (counting
records, not numeric gaps) produces `error{code:"resync_from_head",ref,head_seq}`. Fetch history and
resume from a completed turn. Unknown or mismatched frame versions produce explicit errors.

Each principal shares one logical agent session and one `WatchEvents` subscription across devices.
`msg.send{client_msg_id,text}` forwards to `SendMessage`; accepted or queued `msg.status` frames
correlate the optimistic message. `client_msg_id` is correlation, not durable idempotency: clients
must not automatically resubmit an uncertain send. `msg.abort{turn}` targets that run; zero targets
the current run, including startup. `msg.ack{seq}` acknowledges a session event index, independently
of the transcript cursor. `presence{fg}` is accepted without persistence, and `ping` receives
`pong`. A transport ping runs every 25 seconds. Slow devices are disconnected once their outgoing
queue exceeds 2,048 frames or 8 MiB, so they cannot block another device or the agent.

Live frames reference `msg:{turn,ordinal:-1}`, an ephemeral run draft. `delta.kind` distinguishes
`text`, `reasoning`, `tool_call` and `tool_result`. Tool calls retain raw JSON arguments. A
`snapshot.kind:"model_start"` resets the previous model attempt; text snapshots replace the answer.
A reconnecting device also receives the current answer/reasoning snapshot. Terminal `msg.final`
always carries the full answer and a status (`completed`, `aborted`, `failed`), including partial
answers on abort or failure. Internal bridge errors are not forwarded to the browser. Stream
retention notices remain `notice` frames. Proactive `Deliver` output arrives as a text-only
`notice{code:"outbound",text}` for the owner.

History replaces the transient draft with canonical identities
`(turn, message ordinal, part ordinal)`. Every canonical message's role and every part's type are
preserved, including reasoning, tool calls, tool results and media references. Media transfer itself
is a later slice.

`GET /api/turns?after_seq=42&limit=50` pages newest first, strictly older than `after_seq`. Omit
`after_seq` (or pass zero) for the newest page. `limit` defaults to 50 and accepts 1–500. The
response is `{turns,head_seq,has_more}`. `turns` includes only immutable completed records from
`ListTurns`; the web tier never reads files or manufactures the allocated live turn.

Fixtures in `internal/protocol/testdata` and `internal/server/testdata` freeze the frame table,
message identities, parts, streamed/aborted/failed/resumed turns and history API. The future UI in
`systems/web/ui` should consume these shared fixtures and check its codec against them in CI.
Changes to message identity or part semantics require a browser `v` bump.

## Assets

`internal/assets/dist/` is the shared artifact owned by the serving tier. Only `.gitkeep` is
tracked. The Go binary embeds it with `//go:embed all:dist`. With only that placeholder, `/` serves
a small static stub, `/assets/*` returns 404 and deep-link fallback is inactive. Any actual bundle
must have a nonempty `index.html`, otherwise startup fails. A `Q15_WEB_DIR` override always requires
that index. The override uses a confined filesystem root so symlinks cannot escape it.

A real bundle serves its files and falls back to `index.html` for unknown non-API paths. Unknown
`/api` and `/ws` routes return JSON 404s; missing assets and `/sw.js` never return the SPA. Hidden
paths and directory listings are not served. Hashed files use
`Cache-Control: public, max-age=31536000, immutable`; the index, `/sw.js` and unversioned files use
`no-cache`.

```bash
make build-web-image
```

The image's first build stage is Node 24. When the UI exists, Corepack selects its pinned pnpm from
`ui/package.json`'s `packageManager`; `pnpm install --frozen-lockfile` and `pnpm build` must produce
`internal/assets/dist/index.html`. Until issue #184 creates that UI, the stage carries the
placeholder. The second build stage embeds the output in Go, followed by a minimal nonroot runtime
image. Its UID is 65532 and its GID is 1000, matching the bridge socket's group. Built bundles and
`node_modules` are excluded from Git and Docker input. Main publishes `q15-web` with the existing
synchronized `stable` and DateVer release workflow.

## Validation

```bash
make test-web
make test-web GO='env CGO_ENABLED=1 go' TEST_FLAGS='-race'
make verify
```

Tests exercise a `bufconn` bridge with real `httptest` sockets, a real Unix-socket startup
handshake, policy goldens, gate/origin matrices, multiple devices, stream reconnects, sparse replay
windows, live-turn exclusion, embedded assertions, SPA routing and a disk override containing only
an index.

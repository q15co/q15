# q15-web

The web tier serves the browser socket, transcript API and app from one origin. It authenticates one
owner with WebAuthn and keeps its credential/session store separate from the agent. It dials the
Unix bridge without an agent credential, never mounts memory, and stores no transcript.
Authorization stays behind `gate.Authorizer.RequireScope` and `gate.WithPrincipal`.

## Build and run

```bash
make project-setup
make build-web
Q15_WEB_ORIGIN=http://localhost:8080 Q15_WEB_STATE_DIR="$PWD/.local/web-state" \
  Q15_WEB_BRIDGE=unix:///run/q15/bridge.sock ./bin/q15-web
```

The agent must provision `agent.bridge.listen_target: unix:///run/q15/bridge.sock`. The web process
needs permission to connect. Startup checks the bridge within five seconds and fails on protocol
mismatch. It also refuses invalid, incompatible or unwritable auth state. An empty store starts
locked, with no enrolled devices; it never enables a public first-run setup route.

| Variable                              | Default                       | Purpose                                             |
| ------------------------------------- | ----------------------------- | --------------------------------------------------- |
| `Q15_WEB_LISTEN`                      | `127.0.0.1:8080`              | HTTP listen address                                 |
| `Q15_WEB_BRIDGE`                      | `unix:///run/q15/bridge.sock` | Credential-free bridge socket                       |
| `Q15_WEB_ORIGIN`                      | Required                      | Exact HTTPS public origin, without a trailing slash |
| `Q15_WEB_STATE_DIR`                   | `/var/lib/q15-web`            | Absolute private directory for owner authentication |
| `Q15_WEB_DIR`                         | Embedded assets               | Confined development bundle directory               |
| `Q15_WEB_TLS_CERT`, `Q15_WEB_TLS_KEY` | Unset                         | Optional direct TLS; both must be supplied          |

The public origin must use HTTPS. Only `http://localhost` is allowed for local browser development
(Secure cookies are supported on localhost). The HTTP listener can sit behind host cloudflared,
which terminates TLS. Host and forwarded headers cannot change the configured origin or identity.
Changing the configured origin while retaining a store fails startup; use the original origin or
explicitly reset and re-enroll. Credentials are tied to that origin's hostname as the RP ID.

## Enroll, sign in and revoke

Use a current browser supporting `PublicKeyCredential.parseCreationOptionsFromJSON`,
`parseRequestOptionsFromJSON` and `toJSON`. Every credential requires user verification (PIN or
biometrics) and must be discoverable and device-bound. **Synced/backup-eligible passkeys are
refused**, because revoking a synchronized key cannot revoke one physical device independently. Use
a compatible security key with PIN verification or a device-bound platform authenticator. The
service does not certify hardware provenance: no manufacturer attestation is requested.

Enrollment is a host operation, including for the first device. There is no public enrollment API.
The process exposes `admin.sock` (0600) inside its state directory (0700), never on TCP and never in
the bridge volume. On Hermes, execute the CLI inside the web container:

```bash
docker compose exec q15-web /usr/local/bin/q15-web auth enroll 'Laptop'
```

1. Open the configured public origin on the device to enroll. Its locked page answers **401** and
   shows Sign in plus an **Enroll from Hermes** panel.
1. Paste the options printed by the command into that panel and click Create device credential.
1. Copy the resulting one-line response into the waiting host command within five minutes. Only that
   command submits the registration to the private Unix socket.
1. Click Sign in and complete the authenticator's PIN/biometric check.

The copied JSON contains a challenge and public attestation data, never a private key or session
credential. The public origin can perform a browser ceremony, but cannot register its result: no
forwarded request, first-run race, existing session, or public registration response authorizes
enrollment. The host operator decides which response to accept. Do not accept a response supplied by
someone else. The CLI can be run through an SSH session to Hermes from the device being enrolled.

```bash
docker compose exec q15-web /usr/local/bin/q15-web auth list
docker compose exec q15-web /usr/local/bin/q15-web auth revoke DEVICE_ID
```

Revocation deletes that credential and all its sessions. Other enrolled credentials stay valid. A
lost authenticator cannot sign in again. Host access is the recovery path: enroll a replacement and
revoke the lost device; there is no recovery password. Enroll a spare security key in advance. For
an image-first deployment, add its `--env-file` and `-f` flags to these Compose commands. With
Podman, use `podman exec -it CONTAINER /usr/local/bin/q15-web auth ...`.

## Sessions and HTTP policy

Successful sign-in mints a random 256-bit `__Host-q15s` cookie: Secure, HttpOnly, SameSite=Strict,
Path=/, no Domain. Only its SHA-256 digest persists. No private key or session token is exposed to
JavaScript, browser storage, URLs, referrers or application logs. A passkey assertion is a signed,
single-use challenge response, not a reusable bearer credential. Login challenges are bound to a
separate HttpOnly cookie, expire after two minutes and are consumed even on failed verification.

Sessions expire server-side after **12 hours**, with no sliding renewal. Sign out deletes the server
record and cookie. Sessions grant `chat` only; `console` is refused, including for the owner. A
future console must implement a fresh, time-boxed assertion before adding that scope. Open sockets
recheck sessions before dispatch and output, and at least once per second while idle.
Expiry/revocation closes them with code 4401. The UI stops reconnecting and shows a sign-in link; a
failed upgrade checks `/auth/session` to distinguish authentication loss from a network failure.

Without a valid session, `/`, `/ws`, `/api/turns`, assets and unknown paths answer 401. `/` serves
the same compiled React shell with per-response CSP nonces, rendering sign-in before any chat
adapters are created. Its script, style and fonts are inlined so locked navigation needs no public
asset exceptions. The shell contains no application data, and this trades separate asset caching for
one browser entry and consistent presentation. `/healthz` remains the only unauthenticated
successful resource. The unavoidable proof exchange is `POST /auth/login` (401 plus non-secret
challenge options) and `POST /auth/login/finish` (204 **only after** a valid enrolled WebAuthn
assertion). They do not reach the bridge. `GET /auth/session` and `POST /auth/logout` require a
valid chat session.

All HTTP mutations, including login and logout, require both the exact configured Origin and
`Sec-Fetch-Site: same-origin`. Native WebSocket upgrades require the exact Origin and reject
conflicting Fetch Metadata when present. The security header policy covers errors, assets and
upgrades. The login page uses nonce scripts/styles, without unsafe-inline. There is no password,
Basic, bearer-token, query-token or proxy-identity fallback.

## State, limits and trust

`auth.json` (0600) stores the random owner handle, WebAuthn public credentials/counters and hashed
sessions. Atomic writes and directory sync precede acknowledgment; a persistence error closes the
running authorizer. An exclusive file lock prevents multiple writers. Restart retains credentials
and unexpired sessions, and drops pending ceremonies. The format is versioned; unsupported formats
fail startup. Persist the directory across image updates; back it up privately. Restoring an old
backup can restore previously revoked devices/sessions: revoke them again before exposing service.
Deleting the store removes all access and requires host enrollment again.

The limits are 32 devices, 128 sessions, 64 pending login challenges, five-minute enrollment
ceremonies, 64 KiB ceremony input and a 4 MiB state file. Expired sessions are pruned on writes.
Authentication attempts share a bounded 120-request/minute budget; no forwarded IP headers are
trusted. This bounds work/state rather than promising availability against a distributed attack.
Structured audit events record enrollment, login/refusal, logout and revocation with device IDs.
Logs do not contain cookies, assertions, bodies or transcript data. Configure host log retention
separately.

Host administrators and the web process remain trusted: access to the admin socket or writable store
can enroll an identity. The agent has neither mount. An authenticated XSS can act through the
HttpOnly cookie; it cannot read it. Browser/OS/authenticator compromise, malicious updates and a
malicious TLS terminator are outside this access-control guarantee. **Cloudflare terminates TLS and
can read chat traffic and cookies. This is not end-to-end encryption from Cloudflare.** Model
providers also receive prompts by design. Cloudflare Access is optional defense in depth, not
required by this gate. Tunnel only the public loopback port, never the admin socket or bridge; do
not configure caching of authenticated responses.

Active agent stream interruptions retry after one second with the last received **event index**. An
agent restart invalidates its ephemeral logical session, sends `resync_from_head`, and the next send
allocates a fresh handle; it does not invalidate the independent browser auth store.

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
message identities, parts, streamed/aborted/failed/resumed turns and history API. The UI in
`systems/web/ui` consumes byte-identical copies of these fixtures and checks its codec in CI.
Changes to message identity or part semantics require a browser `v` bump.

## Assets

`internal/assets/dist/` is the shared artifact owned by the serving tier. Only `.gitkeep` is
tracked. The Go binary embeds it with `//go:embed all:dist`. The React PWA in `ui/` produces this
bundle. A missing, empty or partial bundle fails startup; there is no placeholder route. A
`Q15_WEB_DIR` override also requires a nonempty index and uses a confined filesystem root so
symlinks cannot escape it.

A real bundle serves its files and falls back to `index.html` for unknown non-API paths. Unknown
`/api` and `/ws` routes return JSON 404s; missing assets and `/sw.js` never return the SPA. Hidden
paths and directory listings are not served. Hashed files use
`Cache-Control: public, max-age=31536000, immutable`; the index, `/sw.js` and unversioned files use
`no-cache`.

```bash
make build-web-image
```

The image's Node stage reads the shared tool manifest, installs the pinned pnpm and builds the React
PWA with a frozen lockfile. The Go stage embeds the output, followed by a minimal nonroot runtime
image. Its UID is 65532 and its GID is 1000, matching the bridge socket's group. Built bundles and
`node_modules` are excluded from Git and Docker input. Main publishes `q15-web` with the
synchronized `stable` and DateVer release workflow. `q15-web --healthcheck` probes `/healthz`
without adding a shell or HTTP utility to the runtime image.

See [ui/README.md](ui/README.md) for app development, offline fixtures, PWA caching and browser
tests, and [Compose deployment](../../deploy/compose/README.md) for the shared socket volume.

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

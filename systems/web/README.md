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
Path=/, no Domain. Only its SHA-256 digest persists. The cookie identifies a session but cannot
authorize any request alone. Before the one WebAuthn sign-in gesture, the browser generates a
separate ECDSA P-256 key with WebCrypto `extractable: false`. The WebAuthn challenge is SHA-256 over
the UTF-8 tag `q15-session-key-v1\n`, 32 fresh random bytes and that key's SPKI DER bytes, in order.
The server returns the randomness as unpadded base64url `session_key_nonce` alongside `publicKey`
options. Before invoking the authenticator, the browser verifies the challenge against its own
public key and refuses missing, malformed or mismatched commitments. With trusted client code, this
makes the passkey assertion endorse the session key and prevents its substitution in transit. The
server returns a random public binding ID; the browser persists that ID and the opaque,
non-exportable private `CryptoKey` in IndexedDB. This is the only credential-storage exception:
cookies remain HttpOnly, and no raw private key, session token or transcript enters JavaScript
storage, URLs, referrers or application logs. Clearing site storage requires another sign-in. Sign
out removes the stored key. A passkey assertion is a signed, single-use challenge response, not a
reusable bearer credential. Login challenges are bound to a separate HttpOnly cookie, expire after
two minutes and are consumed even on failed verification.

Every protected HTTP request carries a `Q15-Proof` header, containing Unix seconds, a random 128-bit
nonce and a raw 64-byte ECDSA/SHA-256 signature, encoded with unpadded base64url. The signed input
is the newline-separated version tag `q15-proof-v1`, configured origin, public session binding ID,
transport (`http` or `ws`), method, escaped path plus exact query, timestamp and nonce. Proofs
expire 60 seconds after their timestamp; timestamps may be at most five seconds ahead of the server.
The server atomically persists each consumed nonce before dispatch and rejects reuse, including
after restart. Each session retains at most 256 unexpired nonces; a full table refuses new proofs
until entries expire. Expired entries are removed on the next successful write. Invalid proofs
return 401 without changing session state. This binds the method and request target, not the body.

Native WebSocket upgrades offer `q15-auth` plus `q15-proof.<proof>` in `Sec-WebSocket-Protocol`,
because the browser socket API cannot set custom headers. The server selects only `q15-auth`; proofs
never enter URLs or the versioned frame contract. Reconnects generate fresh proofs without another
authenticator gesture. The service worker signs online navigation and shell-cache fetches. Its
initial script fetch cannot set headers either: an authenticated `POST /auth/worker` places an
independently signed, single-use `GET /sw.js` proof in a short-lived HttpOnly `__Host-q15w` cookie.
That cookie is accepted only for that exact GET and is cleared on use. No public worker route is
added. Before a worker controls the page, navigation still returns the locked shell with 401; its
script proves `/auth/session` before rendering chat. If worker installation is unavailable, chat
continues and reloads use the same proof bootstrap.

The compiled shell inlines its favicon so the initial page needs no unsigned icon fetch. A signed-in
page advertises the PWA manifest only after a service worker controls it. First activation claims
uncontrolled pages after authenticated precaching; the worker supplies cached shell assets and signs
cache misses with the session cookie. Initial navigation can still return 401 as described above;
subsequent controlled navigation carries a proof and succeeds with 200.

Sessions expire server-side after **30 days**, with no sliding renewal. Sign out deletes the server
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

`auth.json` (0600) stores the random owner handle, WebAuthn public credentials/counters, hashed
sessions, session public keys/binding IDs and bounded nonce reuse records. Atomic writes and
directory sync precede acknowledgment; a persistence error closes the running authorizer. An
exclusive file lock prevents multiple writers. Restart retains credentials and unexpired sessions,
and drops pending ceremonies. The format is versioned; unsupported formats fail startup. Version 1
stores migrate to version 2 while preserving enrolled devices and discarding all bearer sessions;
users sign in once after the upgrade. Persist the directory across image updates; back it up
privately. Restoring an old backup can restore previously revoked devices/sessions: revoke them
again before exposing service. Deleting the store removes all access and requires host enrollment
again.

The limits are 32 devices, 128 sessions, 64 pending login challenges, five-minute enrollment
ceremonies, 64 KiB ceremony input and a 4 MiB state file. Expired sessions are pruned on writes.
Authentication attempts share a bounded 120-request/minute budget; no forwarded IP headers are
trusted. This bounds work/state rather than promising availability against a distributed attack.
Structured audit events record enrollment, login/refusal, logout and revocation with device IDs.
Logs do not contain cookies, assertions, bodies or transcript data. Configure host log retention
separately.

Host administrators and the web process remain trusted: access to the admin socket or writable store
can enroll an identity. The agent has neither mount. An authenticated XSS can use the session key as
a signing oracle and act as the user, but cannot read/export its private material through WebCrypto
or read the HttpOnly cookie. Non-exportability is a browser API guarantee, not hardware attestation
or protection from copying a browser profile. A stolen cookie alone cannot impersonate the device. A
captured unused proof plus its cookie can race the legitimate request for that exact target within
its short validity window; nonce checks prevent a second acceptance. Browser/OS/authenticator
compromise, malicious updates and a malicious TLS terminator are outside this access-control
guarantee. An active edge serving modified JavaScript can remove the commitment check or use the
session key as a signing oracle; the check does not authenticate application delivery. Cloudflare
terminates TLS and sees cookies and routing metadata, but the content envelope described below hides
chat payloads from passive ingress and web relays. This does not authenticate JavaScript delivery or
resist an active relay substituting key exchange messages. Model providers receive prompts by
design. Cloudflare Access is optional defense in depth. Tunnel only the public loopback port, never
the admin socket or bridge; do not configure caching of authenticated responses.

Active agent stream interruptions retry after one second with the last received **event index**. An
agent restart invalidates its ephemeral logical session, sends `resync_from_head`, and the next send
allocates a fresh handle; it does not invalidate the independent browser auth store.

## Browser contract

The browser contract is version 3, independent of the gRPC protocol version. Every frame is
`{v, id, type, ts, seq, payload}`. `v` is an integer; `ts` is an RFC3339 UTC timestamp. All int64
sequence and cursor values are **decimal JSON strings**, preserving numbers above JavaScript's safe
integer limit. Clients should compare them as `BigInt` values.

| Direction | Types                                                                                                   |
| --------- | ------------------------------------------------------------------------------------------------------- |
| Client    | `hello`, `sync`, `msg.send`, `msg.abort`, `msg.ack`, `msg.status`, `presence`, `ping`                   |
| Server    | `key`, `ready`, `turn.start`, `delta`, `snapshot`, `msg.final`, `msg.status`, `notice`, `pong`, `error` |

The first frame is `hello{cursor,binding,public_key}`. The cursor is zero or the last fully consumed
transcript sequence. The binding is the existing authenticated session binding from #200; the public
key offers a fresh non-exportable P-256 ECDH key. The agent answers
`key{binding,public_key,channel_id}` before replay. The opaque channel handle lasts only as long as
that authorized socket. Each request supplies a nonempty `id` of at most 128 bytes. `sync` requests
the same replay later. Replay emits complete canonical messages oldest first, followed by
`ready{head_seq,cursor,device_id}`. `ready.cursor` is the newest readable record, while `head_seq`
may include a still unwritten live turn. Persist the completed replay cursor after consuming replay;
never advance it merely from the allocated head. More than 500 missed readable turns (counting
records, not numeric gaps), or replay exceeding half the outgoing byte/frame budget, produces
`error{code:"resync_from_head",ref,head_seq}` before any partial replay. Fetch history and resume
from a completed turn. Unknown or mismatched frame versions produce explicit errors.

The agent owns the shared logical session, drafts, replay and one `WatchEvents` subscription per
principal. The web client exposes only `BrowserChannel`, `BrowserHistory` and the version handshake.
Those RPCs carry opaque frame bytes; the existing plaintext RPCs remain available for console
clients and host integrations. Both the browser and bridge protocol versions are 2. After unsealing
in the agent, `msg.send{client_msg_id,text}` calls `SendMessage`; accepted or queued `msg.status`
frames correlate the optimistic message. `client_msg_id` is correlation, not durable idempotency:
clients must not automatically resubmit an uncertain send. `msg.abort{turn}` targets that run; zero
targets the current run, including startup. `msg.ack{seq}` acknowledges a session event index,
independently of the transcript cursor. `presence{fg}` is accepted without persistence, and `ping`
receives `pong`. A transport ping runs every 25 seconds. Slow devices are disconnected once their
outgoing queue exceeds 2,048 frames or `protocol.MaxServerFrameBytes` (about 21.4 MiB), so they
cannot block another device or the agent. This byte budget accommodates one full sealed envelope;
replay reserves half the budget for control frames and concurrent live traffic.

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
`after_seq` (or pass zero) for the newest page. `limit` defaults to 50 and accepts 1–500. Supply
`Q15-Channel` with the active channel handle as well as the usual HTTP proof. The response is a
`history` frame whose sealed payload opens to `{turns,head_seq,has_more}`. `turns` includes only
immutable completed records from `ListTurns`; the web tier never reads files or manufactures the
allocated live turn. Pages stop at a complete turn before their serialized plaintext exceeds 16 MiB,
even if fewer than `limit` records fit. `has_more` remains true; use the oldest returned sequence
for the next request. A single turn larger than this budget returns HTTP 413 `history_too_large`,
without closing the socket.

Canonical fixtures in `libs/chat-contract/browser/protocol/testdata`, the seal package testdata, and
`systems/web/internal/server/testdata` freeze the decoded frame table and a captured sealed
envelope, message identities, parts, streamed/aborted/failed/resumed turns and history API. The UI
in `systems/web/ui` consumes byte-identical copies of these fixtures and checks its codec in CI. The
decoded fixtures test application projections, not plaintext on the wire. The Go-generated sealed
fixture is also opened by WebCrypto. Changes to message identity or part semantics require a browser
`v` bump.

## Content sealing

Sealing is mandatory on browser paths. `msg.send`, `delta`, `snapshot`, `msg.final`, `notice`, and
HTTP `history` seal their complete payloads, including reasoning, tool arguments/results, roles,
part types, media references and model details. Control frames remain visible for routing, ordering,
counting and heartbeat. The web relay never parses a content payload, accumulates a draft, reads
history parts, or has a decryption key. No content or key is stored in its auth state or logged.

Each connection derives directional AES-256-GCM keys from fresh P-256 ECDH and
[HKDF-SHA-256](https://www.rfc-editor.org/rfc/rfc5869). The salt is SHA-256 over the
newline-separated `q15-content-v1`, auth binding, browser public key and agent public key. Browser
private and derived keys are non-exportable [WebCrypto keys](https://www.w3.org/TR/webcrypto/) held
in memory. ECDH derives an opaque HKDF key directly; no shared secret or derived key bytes pass
through JavaScript buffers. No additional passkey gesture or persistent content key is needed:
reload/reconnect establishes new keys and the agent re-wraps plaintext transcript records as they
leave the agent. Logout, expiry and revocation cancel sockets and retire their content channels
through the existing auth model. Host history, exports, scheduled work and transcript schema remain
unchanged.

The generic byte envelope is `{version:1,stream,chunks:[{index,final,data}]}`. `stream` is a random
128-bit base64url ID. Each stream gets a new AES key through HKDF using the handshake salt above.
Info is the newline-separated `q15-content-stream-v1`, direction (`browser-to-agent` or
`agent-to-browser`), and stream ID. Chunks contain at most 32 KiB of plaintext plus a 128-bit GCM
tag; `data` is unpadded base64url. The 96-bit nonce is eight zero bytes followed by the big-endian
uint32 chunk index. A full chunk has `final:false`; the terminal chunk is shorter, possibly empty.
AAD binds the envelope version tag, outer frame version/ID/type/UTC millisecond timestamp/sequence,
stream ID, chunk index and final flag. Reordering, truncation, reflection, tampering and reuse of an
accepted stream are rejected. Content type starts the first encrypted chunk as a big-endian uint16
UTF-8 byte length and the type bytes. Additional content metadata belongs in encrypted bytes. Replay
state is capped at 65,536 accepted streams per direction and connection. The browser starts key
refresh 256 streams before that cap, drains already accepted sends, and reconnects with fresh keys.
Invalid ciphertext still leaves the socket open; a failed decrypt is not a refresh trigger.

The byte primitives read/write bounded chunks; current JSON chat adapters collect only their bounded
text payloads for decoding. `protocol/transport.go` defines the shared byte limits, generated into
TypeScript. Decoded message text is capped at 64 KiB; the derived client wire cap is 528 KiB,
allowing six-byte JSON escapes plus base64 and framing. The browser checks the encoded frame before
sending. Plaintext envelopes are capped at 16 MiB; tags, base64 and framing determine the server
wire/queue cap and bridge receive limit. Oversized live content emits `content_too_large` rather
than silently disconnecting. Only the Go-defined control whitelist travels in plaintext; new or
unknown content frame types are sealed by default. Attachments are not implemented. Future file
transport can use these same byte chunks and put name, type, dimensions and other descriptors inside
the envelope. The carrier must relay bytes without sniffing, transcoding or resizing them.

Invalid ciphertext produces a visible decryption error and leaves the socket open; a later valid
snapshot or history request can recover. Invalid incoming ciphertext produces `unseal_failed`
without accepting or publishing a message. The envelope hides content from passive carriers, not
traffic size/timing or control metadata. It does not encrypt host storage or provider calls, cover
the console, protect compromised browsers/hosts, or resist an actively compromised origin/relay
changing application code or substituting public keys. A future agent identity pin would detect key
substitution only with trusted client code and a trusted initial pin; TOFU would leave the first
exchange exposed. JavaScript served by an active relay could bypass a browser pin check. Independent
agent identity pinning and attachment transfer remain unaddressed. The trusted Unix bridge retains
plaintext console RPCs; restricting the web client interface does not isolate those RPCs from a
compromised web process.

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

## Attachments

The composer holds files in memory until Send. One proof-authorized `POST /api/media` carries a
sealed batch, capped at 16 files and 8 MiB of decoded file bytes total. Raster images larger than
2048 pixels are resized before upload. Attachment-only messages are supported. Agent and web must
upgrade together (bridge and browser protocol version 3).

The agent authenticates the envelope, sniffs bytes, and writes through its runtime media store.
Neither q15-web nor its volume sees plaintext files or descriptors. `GetMedia` streams the existing
32 KiB encrypted chunks through `GET /api/media/<sha256>` for the current content session. Filename,
sniffed type and size are inside the authenticated descriptor; HTTP headers describe only the sealed
response (`attachment`, `nosniff`, sandbox CSP, `no-store`). The browser creates temporary object
URLs after complete authenticated decryption and releases them when a part unmounts. Only
allowlisted raster images and audio render inline; every other kind uses a download link, including
HTML and SVG. Both live and historical messages use the same renderer.

Each upload/send owns a conversation scope. At startup and hourly, the agent retains scopes named by
completed transcript parts and calls `ReleaseAll` for unreferenced web scopes older than 24 hours.
Queuing a previously uploaded ref renews that grace period. Transcript retention therefore controls
referenced media retention; interrupted uploads are swept without a pending-upload store.

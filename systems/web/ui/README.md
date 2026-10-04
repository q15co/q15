# q15 browser app

The React PWA lives here and builds into `../internal/assets/dist`, which `q15-web` embeds. Vite+
provides Vite/Rolldown, Vitest, Oxlint and Oxfmt. Exact Node and pnpm versions live in
`scripts/tool-versions.sh`; package pins are checked against that manifest. No global frontend
tooling is needed.

## Development

```bash
make project-setup
make ui-dev
```

Open `http://127.0.0.1:5173/?preview` for an offline fixture preview. The mock transport replays the
frozen server frames, supports queueing and Stop, and supplies canonical history. It is excluded
from production builds. Component tests use q15 protocol parts directly. Production speaks q15's
versioned WebSocket contract directly.

For a real backend, set `Q15_WEB_ORIGIN=http://localhost:5173` on q15-web with a separate private
development state directory, then open `http://localhost:5173` without `?preview`. Its `/auth`,
`/ws` and `/api` proxies preserve the browser origin. Enroll through the host CLI and sign in with
the device credential; production and the embedded bundle share one origin.

```bash
make ui-lint
make ui-test
make ui-test-coverage
make ui-build
pnpm --dir systems/web/ui exec playwright install chromium
make ui-e2e
make ui-benchmark
make verify
```

On NixOS, set `PLAYWRIGHT_CHROMIUM_EXECUTABLE` to a host Chromium or Chrome executable if the
downloaded browser cannot run. `ui-e2e` tests the compiled production app with fake HTTP/socket
endpoints, including native scroll anchoring and narrow screens. It also starts a local Go fixture
with the real owner gate and private admin socket to verify host enrollment, passkey sign-in and
logout with a Chromium virtual authenticator. Go from `go.work` is required for this gate.

`src/main.tsx` is the sole browser entry. It checks the server session before constructing chat
adapters and renders either `Login` or `App`. The Vite shell plugin inlines compiled script, style
and font assets into one `index.html`; the Go tier adds fresh CSP nonces and returns it with 401
when no request proof is supplied, or 200 when authenticated with a fresh proof. API and static
routes keep their session gate. This avoids a separate login app and unauthenticated asset
exceptions. Build output contains no identity, session credential or transcript. Login/enrollment
I/O lives in an infrastructure adapter behind the `OwnerAuthentication` application port.

`make ui-clean` restores `.gitkeep` and Go packages remain buildable. A runnable web binary needs a
built bundle; `make build-web` builds it first. Build output and dependencies stay ignored, and only
`.gitkeep` is tracked in `internal/assets/dist`. `make test` and `make lint` retain their Go
workflow without installing UI dependencies. The latter still checks all UI file hygiene. Use
`make lint-changed FILES='...'` for TypeScript changes and `make ui-lint` for the full UI gate.

`test:coverage` uses Vite+'s Vitest runner and its matching V8 provider. Run it through
`make ui-test-coverage`; optional `UI_TEST_ARGS` select tests or pass runner options. Coverage
includes all handwritten source, bootstrap, fixture adapter, build plugin and service worker code,
including files that tests never import. Generated contract types and test support are excluded. CI
requires 98% lines, 97% statements, 95% functions and 90% branches overall, plus per-file floors so
aggregate coverage cannot hide an untested module. Every file requires 90% lines, statements and
functions, and 80% branches. Domain files require 98% lines, 95% statements and branches, and 100%
functions; application, infrastructure and shared files require 95% lines and statements, 90%
functions and 85% branches. Both worker entry points require 100% of all four metrics.

Reports stay ignored under `systems/web/ui/coverage/`: open `index.html` for uncovered paths, or use
the LCOV/JSON reports for tooling. The Browser Verify job runs this gate and uploads its reports as
the `browser-coverage` artifact, including on failure. Browser tests remain a separate gate for
native scrolling, layout, keyboard access and compiled PWA behavior.

React Compiler runs in development, tests and production through the React 19 compiler preset.
Streaming drafts live separately from completed history; only the matching turn subscribes to draft
changes. Unchanged parts retain their identities. Markdown updates use a 40ms window and parse the
complete latest source, so closing fences and late reference definitions remain correct; terminal
updates render immediately. Content resize notifications batch bottom-follow work once per animation
frame. Activity row identities survive draft-to-history replacement and resync.

The streaming regression tests compare 100 and 1,000 loaded messages with identical frames, count
Markdown/tool work and grouping visits, and reject any historical collection access by the live
reducer. See [the benchmark](benchmark/README.md) for repeatable browser timing and latency
evidence.

## Architecture and linting

`src/main.tsx` is the composition root: it creates concrete adapters and injects them into the
application. Keep dependencies pointing inward:

| Directory            | Responsibility                                                                                | Allowed dependencies                                                                                   |
| -------------------- | --------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `src/domain`         | Protocol validation, immutable state transitions, history reconciliation and activity pairing | Domain, shared helpers, generated contract types                                                       |
| `src/application`    | Connection lifecycle, subscriptions, sending and history recovery through ports               | Application, domain, shared helpers, generated types                                                   |
| `src/infrastructure` | WebSocket/HTTP adapters and effectful envelope creation                                       | Infrastructure, application ports, domain, shared helpers, generated types                             |
| `src/components`     | React rendering, interaction and colocated styles                                             | Presentation, application, domain, shared helpers and approved UI packages                             |
| `src/shared`         | Small platform-independent codecs and predicates                                              | Shared helpers and generated types                                                                     |
| `worker` / `build`   | Shell caching / bundle generation                                                             | Own modules and shared helpers; worker also allows the proof adapter, build explicit Node/Vite modules |

Native Oxlint `import` rules reject cycles, self imports, duplicates, mutable exports and CommonJS.
Native `no-restricted-imports` allowlists enforce the table, including type imports, re-exports and
literal dynamic imports. A small local TypeScript plugin rejects computed imports and noncanonical
paths that could evade those allowlists. Use explicit `import type` declarations instead of inline
`import()` types. Aliases, parent traversal within paths and importing test modules into production
are rejected. The fixture adapter is the only production-source module allowed to import fixtures;
Vite excludes that adapter from the production bundle.

Domain/shared modules compile separately with only ES2023 types and no browser or Node globals. Lint
also forbids clocks, randomness, timers, browser I/O and parameter mutation there. Application
orchestration may schedule retries and create IDs, but browser I/O must pass through injected ports.
Public state, message collections and pending inputs are readonly. Frozen-input replay tests verify
that reducers and history/activity transformations preserve their inputs.

`lint/config.ts` enables correctness, suspicious, pedantic and performance rules as errors, with
type-aware TypeScript, React hooks/compiler, accessibility, promises, Unicorn and Vitest checks.
Warnings fail the gate. Unsafe assertions, `any`, non-null assertions, unhandled promises,
nonexhaustive switches and accidental implicit truthiness are rejected. The compiler also checks
exact optional properties, indexed access, returns, overrides, unreachable code, side-effect imports
and dependency declarations. Framework/counting-rule exceptions are documented beside their
settings; test-only overrides permit fixtures and mocks while retaining type-safety rules.
Architecture tests run the actual pinned Oxlint against forbidden and permitted dependency probes,
and verify that assertion, `any`, non-null, unsafe assignment and floating-promise rules remain
active in both production and test files. Changing an override cannot silently weaken these guards.

Oxfmt automatically sorts imports into external, relative type, relative value and stylesheet
groups. CSS side-effect imports keep their original execution order. Sorting runs through the
existing Make workflow and is checked by CI:

```bash
make fmt FILES='systems/web/ui/src/components/message.tsx'
make lint-changed FILES='systems/web/ui/src/components/message.tsx'
make ui-fix # Format the whole UI and apply safe lint fixes, then type-check
make ui-lint
```

## Contract and behavior

Fixture copies under `src/fixtures` must be byte-identical to the source JSON in
`../internal/{protocol,server}/testdata`. `src/generated/protocol.ts` is generated from Go's JSON
struct tags:

```bash
python3 scripts/generate-ui-protocol.py > systems/web/ui/src/generated/protocol.ts
make ui-fixtures-check
```

Run those commands from the repository root. CI checks copies and generated types on every PR,
including changes to the fixture producers. Go browser-boundary changes trigger the separate UI job.

JSON fixtures participate in Oxfmt, including their canonical Go sources. After editing a source and
updating its browser copy, format and check both through the shared workflow:

```bash
make fmt FILES='libs/chat-contract/browser/protocol/testdata/frames.json systems/web/ui/src/fixtures/protocol/frames.json'
make lint-changed FILES='libs/chat-contract/browser/protocol/testdata/frames.json systems/web/ui/src/fixtures/protocol/frames.json'
make ui-fixtures-check
```

Incoming JSON remains `unknown` until runtime predicates validate the envelope, event payload and
nested history. Outgoing requests use a discriminated union and a typed constructor that associates
each request with its payload. Pending-message states require a turn ID only once assigned, and
activity entries distinguish tool work from commentary. Event handling checks exhaustiveness;
unknown wire part types remain available to the visible renderer fallback. Shell manifests and
install events are also checked before use. Type-aware Oxc rules reject unsafe assertions and unsafe
uses of `any`, while `satisfies` checks configuration without widening literal values.

One dedicated content worker serves each socket connection and its HTTP history. It owns all content
ECDH/HKDF/AES keys, wire/payload JSON, canonical base64, chunk assembly, UTF-8 and byte-limit
checks. The page sends raw socket text or transferred history buffers and receives validated domain
values or encoded outgoing strings. Socket signing and authenticated HTTP requests remain in the
page; only the public signing-key binding enters the content worker. Reconnect, key refresh and
logout terminate the worker and settle its pending jobs. Fresh non-exportable content keys never
leave that worker or persist. Worker ownership improves scheduling; it is not a security isolation
boundary.

Socket and history operations share one serial queue and replay ledger. Both ends cap queued work at
64 jobs and 96 MiB of conservative input-byte accounting, including the active job. Overflow,
startup failure, malformed RPC, crashes and 15-second deadlines surface a recoverable error and
reconnect. Generation leases discard old history/socket results, and history cancellation suppresses
publication without freeing an active job's budget early. Accepted outgoing sends drain before a
key-budget refresh; uncertain sends are never replayed.

The build bundles `worker/content.ts` into the authenticated shell through a single virtual module.
The bootstrap creates a blob worker, so its first request needs neither service-worker control nor a
separate proof-bearing script fetch. The shell CSP explicitly allows `worker-src 'self' blob:`;
script nonces, static-route authentication and the prohibition on `unsafe-eval` remain intact. The
content worker has no networking, authentication-key or storage dependencies; the separate
`worker/sw.ts` retains its shell-cache and request-proof responsibilities. The Go protocol generates
both byte limits and the plaintext-control whitelist; unknown content types default to sealing.
Before either directional stream count reaches its 65,536-entry replay budget, the transport drains
accepted sends and reconnects with fresh keys. This recovery never resubmits an uncertain send.
History paging is bounded by encoded plaintext bytes as well as turns. Failed decryption is visible
and does not close the socket. See [content sealing](../README.md#content-sealing) for the byte
framing, passive-carrier guarantee and trusted-delivery limits.

The app keeps transcript content and drafts in memory. It never stores messages, raw private keys or
session cookies in localStorage, IndexedDB, or worker caches. The explicit exception is the
non-exportable session `CryptoKey` and its public binding ID in IndexedDB, shared by the HTTP/socket
adapters and the service worker. WebCrypto generates a new ECDSA P-256 key before the single passkey
sign-in gesture. The adapter verifies the WebAuthn challenge's session-key commitment against its
own public key before invoking the authenticator; malformed or substituted commitments refuse
sign-in without a gesture. See [the tier README](../README.md#sessions-and-http-policy) for the
exact hash input and the trusted-client boundary. Subsequent requests and reconnects sign silently.
Proof callers must pass the already escaped same-origin path and query verbatim: the server signs
`RequestURI()`, so a later URL-encoding change causes a 401. Reloads keep the key; logout deletes
it. Injected scripts may use it to sign but cannot export private key bytes. Only the theme
preference uses localStorage. Resume uses `ready.cursor` after replay has been consumed,
independently of live event acknowledgements or the allocated `head_seq`. An uncertain send is shown
explicitly and never resubmitted automatically. Retention gaps refresh completed history and send
`sync` from a readable turn.

Text uses safe Markdown. Each turn groups commentary, reasoning and paired tool calls/results in an
activity disclosure above the final answer. Disclosures start closed, including during active work,
and preserve the reader's choice across live updates and completion. Tool rows summarize the action
and status, with full inputs and outputs one click away. Missing results and unmatched errors remain
visible. Message links open containing disclosures. Media parts show their references until
attachment transfer lands. Unknown parts show their type and raw data. Sending during an active run
uses the server queue; Stop targets the active run, including its startup. History pages backwards
automatically near the top, preserves the visible message, and supports `#message-<turn>:<ordinal>`
links. User bubbles and avatars sit on the right, with left-aligned text and an inset for wrapped
messages. Pending and queued messages use the same layout. The sidebar contains Chat navigation,
connection status and the theme toggle; it does not list individual recent messages.

The manifest is `/manifest.webmanifest` and the service worker is `/sw.js`. The composition root
publishes the manifest link only for a signed-in, controlled page, with
`crossorigin="use-credentials"`. The compiled HTML inlines its favicon to avoid an unsigned initial
fetch. First worker activation claims uncontrolled pages after precaching; updates still wait for
the previous worker's clients to close. Edit the worker in `worker/sw.ts`, which has its own
WebWorker TypeScript environment in `worker/tsconfig.json`. The shell build plugin uses Vite's build
API through Vite+ to compile and bundle the worker, including imports, as a standalone script. It
injects an explicit shell precache manifest and a content-derived cache name after the HTML and
assets have been generated. The worker only handles exact shell paths, ignores queries, and never
handles API, socket or media requests. It reads the opaque session key to sign network requests,
including navigation and precache fetches, and caches only shell responses. Worker registration uses
the authenticated `/auth/worker` handshake described in
[the tier README](../README.md#sessions-and-http-policy); no proof enters a URL. Adapter tests use
fake-indexeddb with real WebCrypto; end-to-end tests exercise Chromium's real key storage, passkey
ceremony, proof replay rejection and navigation across reloads. Existing pages retain their active
worker until they close, avoiding a forced reload during a response.

## Appearance

Component styles live in colocated `*.module.css` files and use Vite's native CSS Modules support.
`src/styles.css` contains only shared theme tokens, base typography, browser resets and
accessibility rules. Buttons use the same module approach; there is no separate utility styling
framework. Keep responsive rules beside the component they affect and use the shared tokens for both
themes.

Shared typography tokens define a small scale, in `rem` so browser font preferences apply:

| Token                       | Default size | Role                                             |
| --------------------------- | ------------ | ------------------------------------------------ |
| `--font-xs`                 | 12px         | Timestamps, status and secondary labels          |
| `--font-sm`                 | 14px         | Controls, navigation, commentary and tool output |
| `--font-md`                 | 16px         | Messages, introductory text and the composer     |
| `--font-lg` / `--font-xl`   | 20px / 24px  | Page and Markdown headings                       |
| `--font-2xl` / `--font-3xl` | 32px / 40px  | Compact / desktop welcome heading                |

Body copy uses a 1.65 line height; controls use 1.5 and headings 1.25. Message and composer widths
share a 48rem maximum. Spacing tokens follow a 4px scale and stay compact as text grows; controls
use minimum heights and grow with their content. Drafts scroll internally after reaching the smaller
of 12rem or 30% of the viewport height.

The app shell provides the named `app` size container. Component modules switch to compact layouts
below 48rem, including when a larger browser font preference increases that threshold. Short
viewports hide the composer footer to preserve room for messages. Browser tests cover doubled text,
keyboard access, narrow screens and long drafts alongside chat behavior.

Theme tokens in `src/styles.css` use the official
[Catppuccin palette](https://github.com/catppuccin/palette) values for Mocha and Latte. The app
follows the system's initial preference and remembers the user's choice. The
[Catppuccin style guide](https://github.com/catppuccin/catppuccin/blob/main/docs/style-guide.md)
defines their roles: Base for the canvas, Mantle for secondary panes, Surface 0 for controls, Text
for body copy, Subtext for labels, Blue for links and pills, and Green/Yellow/Red for status.
Selections use Overlay 2 at 25% opacity and the text cursor uses Rosewater. Pill and warning text
keeps the Text color over an accent tint so the Latte palette remains legible. Small Latte labels
use Subtext 1, and copy on Surface 0 uses Text to preserve contrast.
[Recursive](https://www.recursive.design/) is bundled locally with its full variation axes:
proportional body text and monospace code/tool output. No font or theme asset needs a third-party
request at runtime.

Motion uses lazily loaded Motion animation/gesture features and colocated CSS. The welcome reveals
in stages with a slow decorative orbit; controls have spring feedback, live messages enter gently,
and tool statuses and send/queue icons ease between states. Native disclosures animate their height
where `interpolate-size` and `::details-content` are supported and open instantly elsewhere. History
does not animate on entry, and message links open disclosures instantly before scrolling. A content
resize observer keeps readers at the bottom only while they are following the latest message.

Active work floats gently above a breathing pastel glow: Mauve for thinking and Blue for tool calls.
Progress remains visible with the accordion closed. Motion animates Recursive's Casual, Weight and
Slant axes on short activity labels, the welcome heading and control hover/focus. The Monospace axis
stays at Sans and cursive alternates stay off, preserving label widths and legibility. Transcript
copy, code and input text keep their reading styles. Effects settle when work ends and become static
when reduced motion is enabled.

Theme changes use a circular native View Transition from the theme button, with an immediate
fallback. `prefers-reduced-motion` disables entrance, gesture, disclosure, decoration, theme and
smooth-scroll effects; changing that preference while the app is open also takes effect. Browser
regressions cover that behavior, theme fallback, keyboard disclosures and scroll stability during
live updates.

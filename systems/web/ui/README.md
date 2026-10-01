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
from production builds. Component tests also use the shadcn AI SDK helper to author deterministic
text/reasoning scenarios. Production speaks q15's versioned WebSocket contract directly.

For a real backend, set `Q15_WEB_ORIGIN=http://127.0.0.1:5173` on q15-web and open the dev server
without `?preview`. Its `/ws` and `/api` proxies preserve the browser origin. Authenticate with the
native Basic challenge before using chat; production and the embedded bundle share one origin.

```bash
make ui-lint
make ui-test
make ui-build
pnpm --dir systems/web/ui exec playwright install chromium
make ui-e2e
make verify
```

On NixOS, set `PLAYWRIGHT_CHROMIUM_EXECUTABLE` to a host Chromium or Chrome executable if the
downloaded browser cannot run. `ui-e2e` tests the compiled production app with fake HTTP/socket
endpoints, including native scroll anchoring and narrow screens.

`make ui-clean` restores `.gitkeep` and Go packages remain buildable. A runnable web binary needs a
built bundle; `make build-web` builds it first. Build output and dependencies stay ignored, and only
`.gitkeep` is tracked in `internal/assets/dist`. `make test` and `make lint` retain their Go
workflow without installing UI dependencies. The latter still checks all UI file hygiene. Use
`make lint-changed FILES='...'` for TypeScript changes and `make ui-lint` for the full UI gate.

## Contract and behavior

Fixture copies under `src/fixtures` must be byte-identical to the source JSON in
`../internal/{protocol,server}/testdata`. `src/generated/protocol.ts` is generated from Go's JSON
struct tags:

```bash
python3 scripts/generate-ui-protocol.py > systems/web/ui/src/generated/protocol.ts
make ui-fixtures-check
```

Run those commands from the repository root. CI checks copies and generated types on every PR,
including changes to the fixture producers. Go-only changes do not trigger the separate UI job.

The app keeps transcript content and drafts in memory. It does not store messages or credentials in
localStorage, IndexedDB, or the service worker. Only the theme preference uses localStorage. Resume
uses `ready.cursor` after replay has been consumed, independently of live event acknowledgements or
the allocated `head_seq`. An uncertain send is shown explicitly and never resubmitted automatically.
Retention gaps refresh completed history and send `sync` from a readable turn.

Text uses safe Markdown. Each turn groups commentary, reasoning and paired tool calls/results in an
activity disclosure above the final answer. Active work expands automatically; completed work
collapses. Tool rows summarize the action and status, with full inputs and outputs one click away.
Missing results and unmatched errors remain visible. Message links open containing disclosures.
Media parts show their references until attachment transfer lands. Unknown parts show their type and
raw data. Sending during an active run uses the server queue; Stop targets the active run, including
its startup. History pages backwards automatically near the top, preserves the visible message, and
supports `#message-<turn>:<ordinal>` links. Recent user messages in the sidebar use those links.

The manifest is `/manifest.webmanifest` and the service worker is `/sw.js`. The build emits an
explicit shell precache manifest and a content-derived cache name. The worker only handles exact
shell paths, ignores queries, and never handles API, socket or media requests. Existing pages retain
their active worker until they close, avoiding a forced reload during a response.

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
viewports hide decorative copy and the composer footer to preserve room for messages. Browser tests
cover doubled text, keyboard access, narrow screens and long drafts alongside chat behavior.

The official [Catppuccin palette](https://github.com/catppuccin/palette) supplies Mocha and Latte
theme tokens. The app follows the system's initial preference and remembers the user's choice.
[Recursive](https://www.recursive.design/) is bundled locally with its full variation axes:
proportional body text and monospace code/tool output. No font or theme asset needs a third-party
request at runtime.

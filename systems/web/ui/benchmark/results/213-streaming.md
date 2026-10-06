# Rendered streaming and history latency after #212

The candidate lowers median and p95 arrival-to-visible latency in all three streamed workloads,
under both motion preferences. The final streaming marker and terminal update show no detected
regression. Initial history becomes visible about three times sooner. The main tradeoff is 15% more
main-thread time for large replacement snapshots under reduced motion, because the shorter Markdown
window renders more intermediate versions.

The production change is small: layout/style containment on messages and the working activity, a 24
ms fixed Markdown coalescing window instead of 40 ms, five complete turns on initial/newest history
requests, and ten turns per older page instead of fifty. Complete turns keep prompts, replies and
tool output together. The canonical Markdown parser, terminal flush, content session, protocol and
authorization boundaries are retained.

## Revisions and evidence

- Baseline: merged #212,
  [`f97db79df310dafa8c94fa428667aeecfbcab767`](https://github.com/q15co/q15/commit/f97db79df310dafa8c94fa428667aeecfbcab767).
- Containment and corrected benchmark adapter:
  [`9e5c5dbd7e773e1cce7c2bbb4c4e7cf80b75e092`](https://github.com/q15co/q15/commit/9e5c5dbd7e773e1cce7c2bbb4c4e7cf80b75e092).
- Page sizes and 24 ms window:
  [`c421aa79773a16fd5e0790c68860bf970265913a`](https://github.com/q15co/q15/commit/c421aa79773a16fd5e0790c68860bf970265913a).
- Final streaming measurements and traces:
  [`5f687f1b15649cb669b35a9b0f11b39c35bbf027`](https://github.com/q15co/q15/commit/5f687f1b15649cb669b35a9b0f11b39c35bbf027).
  Its production source is identical to `c421aa79`; it corrects the benchmark producer to enqueue
  the terminal event immediately after the last live frame completes.

The raw results behind these tables are not committed. Each run retains every measured repetition,
actual arrivals, codec completions, marker latencies, long tasks and render/Markdown work counts,
and the commands under [Reproduction](#reproduction) regenerate them:

- Large snapshots, growing GFM and history during streaming: six pairs per workload and motion at
  `5f687f1b`.
- Startup: six pairs per motion at `c421aa79`, with the same production source.
- Codec controls: six pairs at the containment-only production source. The recorded staged tree
  `b6a83513a35bcad9395ed3c0341a4c72120c32cc` is exactly the tree committed as `9e5c5dbd`.
- Containment-only comparison and the delayed-terminal control: earlier complete timing runs,
  retained at the time to make the selection process and harness correction inspectable.
- Profile results: a separate one-pair trace run at `5f687f1b` with `Q15_BENCHMARK_TRACE=1`. Trace
  timings are excluded from the comparison tables.

The runner now preserves the selected revision's codec adapter before copying the shared harness. In
particular, the #212 baseline uses `parseFrameValue` and native base64. The legacy stringify/parse
adapter is used only when the selected revision predates that adapter. A regression test covers both
cases. Both builds use their selected production `ContentSession`, protocol and history adapter.

## Method

Measured on 2026-10-05: Intel i9-10885H @ 2.40 GHz, NixOS/Linux 6.18.54, headless Chrome
154.0.8037.92, 1280 × 900 viewport, pinned Node 24.21.0 / pnpm 12.8.1 / Vite+ 1.0.0. Builds finish
before measurement. Each workload/motion has one unrecorded warmup per revision followed by six
pairs, alternating baseline/candidate and candidate/baseline. Runs are sequential with no concurrent
builds or tests; normal desktop background activity is not eliminated. These are device results, not
CI timing thresholds or a proof for other hardware.

Streamed workloads mount 1,000 completed messages before timing. Large snapshots deliver 48
approximately 67.5 kB replacement frames; growing GFM delivers 24 text frames with periodic
replacement snapshots, incomplete fences, nested lists, tables and late reference definitions.
History paging starts at the seventh GFM frame. The sealed terminal event immediately flushes the
complete final answer. Peer sealing, fixture delivery, key exchange, initial history and font
loading finish before the streaming timer starts.

The producer requests 16 ms intervals on the browser main thread. Congestion delays the producer as
well as rendering, so raw results include actual arrival times; this is not an independent socket
arrival clock. Codec completion includes incoming queue wait, decryption and validation. The
requestAnimationFrame sampler measures first DOM appearance of every marker, including markers
skipped by coalescing. This is a rendering/paint opportunity, not compositor, display or viewport
visibility proof. Median/p95 pool the equally sized marker sets from six runs using nearest rank
(`ceil(p * n) - 1`); other durations and work counts are arithmetic means per run. Long-task counts
and durations are totals across all six runs. CDP main-thread time includes setup of the live
turn/tool and the trailing 100 ms observer drain. Do not add nested trace/Markdown timings to CDP
task time.

Startup begins with the fonts and empty app shell ready and includes the first history HTTP request
and rendering. The standalone harness excludes passkey/login and service-worker startup; browser
tests cover the production authenticated gate separately. Startup and paging use the actual signed
HTTP adapter, the same content session/replay ledger as incoming frames, and a peer that honors the
selected adapter's cursor/limit. HTTP completion includes proof generation, request handling, peer
sealing, decoding and validation. Paging directly invokes the store, excluding the transcript's
anchor acquisition; end-to-end tests verify the actual button and preserved reader position.

## Streaming results

All values are milliseconds, baseline → candidate. The last marker is measured separately from the
sealed terminal update, which bypasses the Markdown waiting window.

| Motion  | Workload            | Median visible | p95 visible   | Last streaming marker | Terminal visible |
| ------- | ------------------- | -------------- | ------------- | --------------------- | ---------------- |
| Normal  | Large snapshots     | 108.0 → 56.5   | 176.1 → 122.3 | 66.2 → 52.0           | 57.4 → 45.4      |
| Normal  | Growing GFM         | 80.8 → 35.6    | 150.3 → 82.5  | 45.9 → 38.8           | 44.2 → 37.4      |
| Normal  | GFM + older history | 90.3 → 38.7    | 280.8 → 108.6 | 54.2 → 37.9           | 52.5 → 36.3      |
| Reduced | Large snapshots     | 63.1 → 54.3    | 118.3 → 105.7 | 64.8 → 46.1           | 62.8 → 44.3      |
| Reduced | Growing GFM         | 47.1 → 35.2    | 90.3 → 69.3   | 41.3 → 35.0           | 37.1 → 33.6      |
| Reduced | GFM + older history | 48.3 → 39.8    | 326.4 → 132.1 | 40.3 → 39.7           | 38.8 → 38.2      |

Per-run p95 decreases in all six pairs for each normal-motion workload and reduced-motion snapshots,
and five of six pairs for reduced-motion GFM and paging. Paired bootstrap percentile 95% intervals
for the mean per-run p95 difference are entirely below zero in every case: normal snapshots \[−66.7,
−34.6\], GFM [−78.2, −53.4], paging [−184.5, −85.7] ms; reduced snapshots [−17.6, −5.2], GFM
\[−27.7, −3.6\], paging [−244.8, −77.4] ms. These use 10,000 resamples with seed 213, retaining
every pair.

For the last marker, paired mean candidate-minus-baseline differences are −14.2, −7.2 and −16.3 ms
with normal motion; −18.6, −6.3 and −0.6 ms with reduced motion. A paired bootstrap (10,000
resamples, seed 213, percentile 95% interval) gives respectively [−27.9, −1.3], [−14.6, −0.1],
[−23.0, −10.5], [−38.2, −0.6], [−12.3, −1.0] and [−8.0, +6.6] ms. Reduced-motion paging's last
marker and terminal are effectively unchanged within observed variation; one candidate paging run
recorded 1,445 ms main-thread time versus 872–959 ms in its other runs. It is retained in every
statistic. Six pairs do not establish a universal timing guarantee, particularly for differences
below one animation frame.

| Motion  | Workload            | Main-thread time | Long tasks: count / total ms | p95 codec completion |
| ------- | ------------------- | ---------------- | ---------------------------- | -------------------- |
| Normal  | Large snapshots     | 3385.8 → 2446.8  | 15 / 1048 → 7 / 394          | 1.1 → 1.0            |
| Normal  | Growing GFM         | 1653.3 → 1139.4  | 10 / 653 → 0 / 0             | 0.7 → 0.5            |
| Normal  | GFM + older history | 1962.6 → 1234.9  | 21 / 2045 → 0 / 0            | 0.8 → 0.9            |
| Reduced | Large snapshots     | 1609.6 → 1853.8  | 15 / 940 → 5 / 260           | 1.2 → 1.0            |
| Reduced | Growing GFM         | 854.5 → 797.2    | 8 / 535 → 0 / 0              | 0.6 → 0.6            |
| Reduced | GFM + older history | 1158.6 → 996.5   | 20 / 1961 → 4 / 236          | 0.9 → 0.9            |

| Motion  | Workload            | Actual mean arrival interval | Stream elapsed  |
| ------- | ------------------- | ---------------------------- | --------------- |
| Normal  | Large snapshots     | 64.4 → 46.5                  | 3199.0 → 2283.3 |
| Normal  | Growing GFM         | 62.1 → 40.7                  | 1557.1 → 1015.0 |
| Normal  | GFM + older history | 75.3 → 44.8                  | 1883.7 → 1161.7 |
| Reduced | Large snapshots     | 36.3 → 42.5                  | 1876.1 → 2090.3 |
| Reduced | Growing GFM         | 35.7 → 34.1                  | 935.7 → 859.4   |
| Reduced | GFM + older history | 47.0 → 39.3                  | 1199.6 → 1002.5 |

The reduced-motion snapshot workload spends 15.2% more main-thread time and takes 11.4% longer
overall while lowering median/p95 visibility and final-update latency. It parses about 25 versions
instead of 15. This is a deliberate latency/CPU tradeoff, not an across-the-board throughput gain.

## History results

Initial history requests return fifty turns in the baseline and five in the candidate. The fixture
has one assistant message per turn; the browser pagination test separately exercises two messages
per complete turn.

| Motion  | Initial HTTP completion | History DOM visible | Main-thread time | Long tasks: count / total ms |
| ------- | ----------------------- | ------------------- | ---------------- | ---------------------------- |
| Normal  | 11.3 → 10.9             | 175.3 → 63.0        | 224.3 → 76.1     | 6 / 976 → 6 / 308            |
| Reduced | 9.3 → 8.1               | 161.1 → 54.9        | 212.6 → 69.9     | 6 / 915 → 0 / 0              |

During GFM streaming, older pages contain fifty turns in the baseline and ten in the candidate:

| Motion  | Older HTTP completion | Older history DOM visible | New historical message renders |
| ------- | --------------------- | ------------------------- | ------------------------------ |
| Normal  | 293.1 → 155.1         | 430.2 → 191.5             | 50 → 10                        |
| Reduced | 39.7 → 87.9           | 149.0 → 143.9             | 50 → 10                        |

The slower mean reduced-motion HTTP completion includes the retained outlier and queueing behind
stream rendering. Request completion alone is not a visibility metric. Five-turn startup produces 10
Markdown parses instead of 100: Markdown processing averages 72.7 → 21.6 ms normally and 69.9 → 20.2
ms with reduced motion. Paging includes 20 historical Markdown parses instead of 100.

## Attribution and implementation choice

Markdown parse/process/JSX conversion is measured by identical benchmark-only instrumentation in
both builds. React message render counts are also recorded. No probes enter the production bundle.

| Motion  | Workload            | Markdown parses | Markdown processing ms | Live message renders |
| ------- | ------------------- | --------------- | ---------------------- | -------------------- |
| Normal  | Large snapshots     | 22.2 → 25.0     | 747.3 → 774.8          | 49 → 49              |
| Normal  | Growing GFM         | 12.0 → 13.0     | 152.7 → 145.0          | 25 → 25              |
| Normal  | GFM + older history | 111.8 → 33.0    | 203.0 → 154.5          | 25 → 25              |
| Reduced | Large snapshots     | 15.0 → 25.0     | 457.2 → 729.7          | 49 → 49              |
| Reduced | Growing GFM         | 8.8 → 13.0      | 81.6 → 121.7           | 25 → 25              |
| Reduced | GFM + older history | 109.3 → 33.0    | 139.0 → 150.7          | 25 → 25              |

Historical messages already mounted before streaming render zero additional times in either
revision. Thus the normal-motion cost is not explained by React rerendering the completed
transcript. The separate timeline profiles retain sampled JavaScript stacks, Layout,
UpdateLayoutTree, PrePaint and Paint events to distinguish browser rendering from codec and Markdown
processing. The largest avoidable normal-motion cost is in PrePaint:

| Motion  | Workload            | Profile PrePaint ms | Profile Layout ms |
| ------- | ------------------- | ------------------- | ----------------- |
| Normal  | Large snapshots     | 1032.4 → 60.2       | 525.3 → 519.3     |
| Normal  | Growing GFM         | 527.3 → 36.9        | 251.2 → 288.3     |
| Normal  | GFM + older history | 640.3 → 40.3        | 301.6 → 259.6     |
| Reduced | Large snapshots     | 217.0 → 23.9        | 107.8 → 158.4     |
| Reduced | Growing GFM         | 141.1 → 17.4        | 68.1 → 83.4       |
| Reduced | GFM + older history | 241.9 → 23.0        | 91.9 → 102.4      |

These are single profiled runs, summing `ph: "X"` durations by event name on `CrRendererMain`, not
the six-run timing comparison. Lower PrePaint with unchanged historical render counts supports
containing browser traversal across completed rows. The larger normal-motion PrePaint cost is
consistent with the ongoing font-axis animation amplifying that work. Layout and Markdown still grow
with additional intermediate parses; moving codec processing would leave these phases on the main
thread.

`contain: layout style` scopes that invalidation while preserving content-driven height and
overflow. It does not enable size or paint containment, clipping or virtualization. See the
[CSS containment specification](https://www.w3.org/TR/css-contain-2/#layout-containment). Font-axis
animation remains enabled and reduced-motion preference changes remain effective while the app is
open. Paging reduces actual inserted rows; it does not evict previously read history.

Containment alone substantially improved normal-motion rendering, but did not consistently improve
the remaining coalescing/animation-frame wait with reduced motion. Shortening the fixed window to 24
ms improves the visibility distribution while still coalescing bursts. Final Markdown is parsed
canonically; no prefix cache can miss a late reference definition. Terminal events flush
immediately. An earlier driver paused for an extra 16 ms after the last live frame before delivering
the terminal event. That phase-sensitive control gave worse last-marker latency in reduced-motion
snapshots/GFM despite improved pooled p95. The final driver removes that artificial terminal pause
for both revisions. The earlier results are retained above; the final-marker claim applies to the
immediate terminal workload and is not a claim about every possible provider terminal delay.

The corrected codec controls recorded no tasks over 50 ms in either revision:

| Motion  | Codec workload                   | Main-thread time, baseline → containment candidate |
| ------- | -------------------------------- | -------------------------------------------------- |
| Normal  | 48 tiny incoming frames          | 13.0 → 12.2                                        |
| Normal  | 48 large incoming snapshots      | 38.1 → 35.9                                        |
| Normal  | Sealed 4,000-turn page           | 41.3 → 42.2                                        |
| Normal  | 48 outgoing 64,000-byte messages | 45.5 → 45.9                                        |
| Reduced | 48 tiny incoming frames          | 10.9 → 11.4                                        |
| Reduced | 48 large incoming snapshots      | 34.5 → 34.1                                        |
| Reduced | Sealed 4,000-turn page           | 39.0 → 39.4                                        |
| Reduced | 48 outgoing 64,000-byte messages | 45.0 → 44.0                                        |

Concurrent codec-only incoming bursts have queue-inclusive p95 completion around 4 ms for tiny
frames and 26–28 ms for large snapshots; paced rendered frames complete in 0.5–1.2 ms at p95. The
remaining dominant costs are browser rendering, canonical Markdown work and its waiting window. A
content worker would retain main-thread layout/prepaint/DOM work and add structured clone/transfer,
RPC validation, scheduling and queue waits to that sub-millisecond paced codec path. Those added
costs were not measured because no new worker was implemented. The retained main-thread session
avoids a new key/replay lifecycle and pending-job boundary. A Markdown worker would require moving a
different parse representation plus reconstruction; the smaller containment/window/page changes
already address measured visibility, so that larger design is not justified by this patch.

## Reproduction

Run from the repository root with a committed candidate:

```bash
make project-setup
export PLAYWRIGHT_CHROMIUM_EXECUTABLE=/path/to/chrome
export Q15_BENCHMARK_CODEC=1
export Q15_BENCHMARK_BASE=f97db79df310dafa8c94fa428667aeecfbcab767
export Q15_BENCHMARK_RUNS=6
export Q15_BENCHMARK_WORKLOAD='codec=1&startup=1,codec=1&rendered=1,codec=1&rendered=1&representative=1,codec=1&rendered=1&representative=1&paging=1'
Q15_BENCHMARK_OUTPUT=benchmark-results/213-repeat.json make ui-benchmark
Q15_BENCHMARK_TRACE=1 Q15_BENCHMARK_RUNS=1 \
  Q15_BENCHMARK_OUTPUT=benchmark-results/213-profile/results.json make ui-benchmark
```

Run profiling separately from untraced timing, with no concurrent builds/tests. To repeat codec
controls, select `codec=1&small=1,codec=1&large=1,codec=1&history=1,codec=1&outgoing=1`. The current
runner's default codec workload list includes all controls and rendered scenarios.

## Behavioral and repository validation

All passed on the final production source:

- `make project-setup` and the required `make fmt FILES=...` / `make lint-changed FILES=...` loop.
- `make ui-lint`, including deterministic-domain and service-worker type checks.
- `make ui-test-coverage`: 175 tests, 99.37% lines and 94.66% branches; all per-file floors and
  architecture probes pass. Coverage includes canonical final Markdown with 500 fences/tables,
  bounded snapshot rendering and unmount cancellation, immediate terminal reconciliation, UTF-8
  byte-limit rejection, composer draft retention, and corrected baseline adapter selection.
- `PLAYWRIGHT_CHROMIUM_EXECUTABLE=... make ui-e2e`: 18 tests, including the new five-turn startup /
  ten-turn paging case with two messages per turn and a stable reader anchor. Existing tests cover
  authenticated uncontrolled startup, reload and controlled PWA requests through the production
  gate, shell-only offline caching, deep links, streaming/disclosure scroll behavior, changing
  motion preference, working font animation, and recovery from a damaged sealed frame.
- `make ui-fixtures-check`: canonical browser fixtures and generated types match.
- `make build-web-image`: production UI and Go embedding build as `q15-web:local`, image
  `sha256:18b71bec6c4eb8f03ce65409a2e855ec38fd127ac672c8f50b1478324a9d9a7d`.
- `make compose-check`: rendered Compose and bridge-volume isolation contract pass.
- `make verify`: generated protocol check, repository lint/static analysis and all Go/contract
  tests.

Image and Compose checks validate the build and repository deployment contract. They do not claim
live activation on another host. Raw streaming rows, earlier controls and all sixteen compressed
traces were compared with the original runner outputs after formatting; the traces themselves stay
local.

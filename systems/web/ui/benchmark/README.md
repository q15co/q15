# Streaming benchmark

See [the recorded comparison](results/2026-10-04.md) for device results and raw measurements. The
[post-#212 comparison](results/213-streaming.md) profiles the remaining rendered costs.

Run from the repository root with the pinned toolchain:

```bash
make project-setup
Q15_BENCHMARK_BASE=19c24541 make ui-benchmark
```

On NixOS, set `PLAYWRIGHT_CHROMIUM_EXECUTABLE` to a host Chrome/Chromium executable. The runner
creates a temporary detached baseline worktree, copies the same harness/workload into it, and builds
both revisions with production React. It uses the current pinned dependencies for both builds; the
baseline retains its original compiler configuration. It removes the temporary worktree afterward.
It serves only the standalone benchmark on `127.0.0.1:4195` (candidate) and `4196` (baseline),
without a backend, login or service worker. No benchmark entry is included in the application
bundle.

The three workloads use 100 and 1,000 completed messages with identical live text/reasoning frames,
then a large growing answer with empty history. History contains Markdown, reasoning, tool input and
output. Each active turn includes a tool call/result. Text alternates deltas with replacement
snapshots every tenth update; reasoning does the same. The large answer crosses incomplete fences,
nested lists, tables and references. Every source slice also adds a numbered marker. The final event
flushes the entire answer. Unit tests separately compare final HTML with unthrottled canonical
Markdown.

The sender requests one update every 16ms: 24 text/reasoning pairs for the history workloads and 120
for the large answer. Actual arrival timestamps and elapsed time are recorded because main-thread
congestion can delay the producer too. A requestAnimationFrame sampler measures each marker's first
appearance in the rendered answer, including markers skipped by intermediate coalescing. Latency
includes waiting for rendering and the next animation frame; it is a paint opportunity, not a
display or compositor measurement. Hidden reasoning does not satisfy the visibility check. Initial
history loading and font loading finish before counters start. Final reconciliation is included.

Chrome DevTools Protocol supplies aggregate main-thread task, script, layout and style time.
PerformanceObserver records tasks longer than 50ms. The harness adds the same marker inspection
overhead to both revisions. Socket delivery and encryption are excluded to measure #208
independently of #209. Normal and reduced motion are measured separately. An additional
normal-motion workload fixes only the working label's font axes via CSS, leaving Motion and the
other animations running, to assess font shaping/layout separately.

Elapsed time covers the first text frame through final rendering. CDP counters also include live
turn/tool setup and the trailing 100ms observer drain.

Each workload and motion preference starts with an unrecorded warmup for each revision. Subsequent
pairs alternate baseline/candidate and candidate/baseline order. Default output is ignored under
`benchmark-results/results.json`; set `Q15_BENCHMARK_OUTPUT` for another path and
`Q15_BENCHMARK_RUNS` to change the default three repetitions. Run comparisons without concurrent
builds/tests. Wall-clock results are device evidence. CI gates the deterministic work counts and
behavior tests, without machine-dependent timing thresholds.

Commit or stage the measured changes before running. Results identify an uncommitted head by its
staged Git tree and mark `headDirty`; committed heads use the commit revision.

## Codec measurements (#209)

See [the recorded codec comparison](results/209-codec.md) for device results and raw measurements.
Compare the codec-only optimizations against the merged #208 revision:

```bash
Q15_BENCHMARK_CODEC=1 Q15_BENCHMARK_BASE=754b7f123f7389a8eb5f310904896738cbda9c10 \
  Q15_BENCHMARK_RUNS=3 Q15_BENCHMARK_OUTPUT=benchmark-results/209-codec.json make ui-benchmark
```

Both revisions use their production ContentSession, the same post-#208 React Compiler and pinned
dependencies. The selected revision's codec adapter is preserved: revisions before #212 retain the
decoded-frame stringify/parse round trip; #212 and later validate the decoded object directly.
Native base64 runs where the browser supports it, with a canonical fallback elsewhere. Live-frame
fixture delivery and peer sealing, key exchange, preloaded historical rows and font loading finish
before timing. No worker is involved in either build.

Workloads isolate 48 tiny incoming frames, 48 approximately 67.5 kB replacement snapshots, a sealed
4,000-turn history page and 48 outgoing 64,000-byte messages. Separate rendered workloads deliver 48
large snapshots or 24 growing GFM text frames with periodic snapshots into the actual app with 1,000
loaded messages, requesting 16 ms arrival intervals. Markers record actual arrival-to-visible
latency and include the selected revision's Markdown coalescing window. The producer can be delayed
by main-thread congestion; its clock is not an independent socket clock. CDP
task/script/layout/style time, long tasks, codec completion and visible latency are recorded
separately.

Rendered workloads now include a sealed terminal event and distinguish the last streaming marker
from terminal visibility. The paging workload calls the production history adapter at the seventh
live frame, with 1,000 completed messages already mounted. Startup (`codec=1&startup=1`) starts with
an empty transcript and measures the selected revision's actual initial HTTP page size. Both HTTP
workloads use authenticated fetch, the same content session as incoming frames, and a sealing peer
that honors the requested cursor and limit. HTTP completion includes proof generation, request, peer
sealing, decoding and validation; history DOM visibility is recorded separately. The benchmark build
instruments Markdown parse/JSX conversion duration and live/historical message render counts; those
probes are absent from the application build. Initial loading is excluded for streaming workloads,
while final reconciliation and the trailing 100 ms observer drain are included. Startup excludes the
initial font and empty-shell load, and includes the first history request and render.

Use `Q15_BENCHMARK_TRACE=1` in a separate run to capture the first recorded repetition for each
revision/workload/motion with CDP timeline events, user timing and sampled CPU stacks. Compressed
Chrome traces go into `traces/` beside the result JSON; decompress them before loading in Chrome
DevTools or Perfetto. Profiling adds overhead, so use an untraced run for timing comparisons.

Set `Q15_BENCHMARK_WORKLOAD` to comma-separated query strings to select workloads. Output paths are
relative to the UI directory. Commit the measured implementation before running so the report
identifies a revision available in the PR history.

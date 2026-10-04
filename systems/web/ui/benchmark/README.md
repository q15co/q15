# Streaming benchmark

See [the recorded comparison](results/2026-10-04.md) for device results and raw measurements.

Run from the repository root with the pinned toolchain:

```bash
make project-setup
Q15_BENCHMARK_BASE=19c24541 make ui-benchmark
```

On NixOS, set `PLAYWRIGHT_CHROMIUM_EXECUTABLE` to a host Chrome/Chromium executable. The runner
creates a temporary detached baseline worktree, copies the same harness/workload into it, and builds
both revisions with production React. It uses the current pinned dependencies for both builds; the
baseline retains its original compiler configuration. It removes the temporary worktree afterward.
It serves only the standalone benchmark on `127.0.0.1:4195`, without a backend, login or service
worker. No benchmark entry is included in the application bundle.

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

Default output is ignored under `benchmark-results/results.json`; set `Q15_BENCHMARK_OUTPUT` for
another path and `Q15_BENCHMARK_RUNS` to change the default three repetitions. Run comparisons
without concurrent builds/tests. Wall-clock results are device evidence. CI gates the deterministic
work counts and behavior tests, without machine-dependent timing thresholds.

Commit or stage the measured changes before running. Results identify an uncommitted head by its
staged Git tree and mark `headDirty`; committed heads use the commit revision.

## Content worker measurements (#209)

Compare against the merged #208 revision, rather than combining render and codec changes:

```bash
Q15_BENCHMARK_CODEC=1 Q15_BENCHMARK_BASE=754b7f123f7389a8eb5f310904896738cbda9c10 \
  Q15_BENCHMARK_RUNS=3 make ui-benchmark
```

The codec harness creates a real encrypted peer in the Node runner. Peer sealing, worker startup,
key exchange, fixture loading and initial React/history/font work finish before timing. Its baseline
adapter reproduces the previous main-thread codec and socket ordering; the current adapter uses the
production dedicated worker. Both use the same post-#208 React Compiler and dependencies.

See [the recorded worker comparison](results/209-content-worker.md) for measurements and
limitations.

Workloads isolate 48 tiny incoming frames, 48 approximately 67.5 kB replacement snapshots, a sealed
4,000-turn history page, and 48 outgoing 64,000-byte messages. A separate rendered stream delivers
48 large snapshots to the actual app with 1,000 loaded messages, requesting 16 ms arrival intervals.
Snapshots retain all previous markers. The sampler waits for the final marker, including the 40 ms
Markdown window, and records every marker's first rendered visibility. Main-thread congestion can
delay the producer; timings describe actual arrival-to-visibility, not an independent network clock.
A second rendered workload uses 24 growing GFM text frames with periodic snapshots, totaling about 4
KiB, against the same 1,000-message history. Set `Q15_BENCHMARK_WORKLOAD` to comma-separated exact
queries from the runner to select workloads; output paths are relative to the UI directory.

CDP task/script/layout/style time and long-task durations remain separate from codec completion and
visible latency. Worker samples record execution wall time and RPC round-trip time. Their difference
includes queue wait, structured cloning, scheduling and page validation; it does not isolate clone
CPU cost. WebCrypto wait is included in execution wall time. History input transfers its
ArrayBuffer; returned domain objects and socket strings use structured clone. Codec-only bursts stay
within the production queue limits. Worker timings exclude initial handshake and setup.

Socket networking, owner login, startup and background-tab behavior are excluded from these timing
runs and covered by the real-gate browser tests. These synthetic workloads establish device
evidence, not universal latency guarantees or CI timing thresholds.

# Streaming benchmark

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

Default output is ignored under `benchmark-results/results.json`; set `Q15_BENCHMARK_OUTPUT` for
another path and `Q15_BENCHMARK_RUNS` to change the default three repetitions. Run comparisons
without concurrent builds/tests. Wall-clock results are device evidence. CI gates the deterministic
work counts and behavior tests, without machine-dependent timing thresholds.

Commit or stage the measured changes before running. Results identify an uncommitted head by its
staged Git tree and mark `headDirty`; committed heads use the commit revision.

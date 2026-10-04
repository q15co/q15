# Content worker comparison: issue #209

Measured on 2026-10-04 on an Intel Core i9-10885H (8 cores / 16 logical processors), NixOS 26.11 /
Linux 6.18.54, Chrome 154.0.8037.92 headless, 1280 × 900, without CPU throttling. Both builds use
pinned Node 24.21.0, pnpm 12.8.1 and Vite+ 1.0.0. Three runs per revision, workload and motion
preference; no concurrent builds or tests during measurement.

Baseline: merged #208 commit `754b7f123f7389a8eb5f310904896738cbda9c10` with React Compiler. Worker
measurement: staged tree `21bcebb8120371ee316db95e974968a94893e01e`. Rendered streams: staged tree
`d257afaaae382f2c19702f71938aa180b66e8fb6`. Both trees contain the same measured production codec;
the latter adds the representative workload, corrects history-buffer length reporting, waits for all
historical rows and fonts before measurement, and delays refresh notifications until socket controls
drain. Raw evidence: [codec and initial stress stream](209-content-worker.json),
[settled rendered streams](209-streaming.json).

## Codec cost

Mean Chrome main-thread task time, normal motion, in milliseconds:

| Workload                                       | Main-thread baseline | Content worker | Reduction |
| ---------------------------------------------- | -------------------: | -------------: | --------: |
| 48 tiny incoming frames                        |                 31.2 |           19.4 |       38% |
| 48 approximately 67.5 kB replacement snapshots |                457.6 |           28.8 |       94% |
| Sealed 4,000-turn history                      |                342.4 |           40.0 |       88% |
| 48 outgoing 64,000-byte messages               |                212.0 |           28.7 |       86% |

Each baseline large/history/outgoing run produced one main-thread task longer than 50 ms, with
maximum durations of 448 / 340 / 201 ms respectively. Their worker runs produced none. Tiny-frame
runs produced none in either implementation. Reduced-motion codec results are similar. History
rendering is excluded from the history codec workload.

Worker execution wall time averages 0.1 ms per tiny frame, 0.8 ms per large frame, 43.2 ms for the
history page and 0.9 ms per outgoing message. Mean RPC round trips are 3.8 / 24.6 / 74.5 / 1.1 ms
respectively. The large-frame burst queues 48 jobs; round trips include queue wait, cloning,
dispatch and page validation, so their difference is not clone CPU time. WebCrypto wait is included
in worker wall time. Keys and peer fixtures are prepared before timing. Native Uint8Array base64
methods are used where supported, with a canonical validated fallback. WebCrypto already executes
crypto work asynchronously; these results include moving the codec glue and JSON off the page,
native base64 and eliminating the decoded-frame stringify/parse round trip. They do not attribute
all savings to moving AES or to worker scheduling alone.

The first raw report records zero head history bytes because transferring the buffer detached it
before the length was sampled. The input was identical in both builds: 3,168,957 encoded bytes. The
harness now retains its original length before transfer.

## Rendering and visibility

Mean main-thread task time and pooled marker arrival-to-visibility latency in milliseconds.
Percentiles select the lower indexed value from the sorted samples; visibility is a
requestAnimationFrame paint opportunity, not display/compositor timing. Markdown's existing 40 ms
window is included.

| Rendered workload                                                 | Motion  | Main-thread baseline → worker | Median latency baseline → worker | p95 latency baseline → worker |
| ----------------------------------------------------------------- | ------- | ----------------------------: | -------------------------------: | ----------------------------: |
| 48 large replacement snapshots, 1,000 history messages            | Normal  |             3,814.6 → 3,691.3 |                    109.8 → 162.8 |                 186.3 → 226.7 |
| Same stress stream                                                | Reduced |             2,025.2 → 2,093.4 |                      72.1 → 93.5 |                 129.0 → 148.6 |
| 24 growing GFM frames, periodic snapshots, 1,000 history messages | Normal  |             1,891.1 → 1,844.7 |                     84.1 → 126.1 |                 139.2 → 201.4 |
| Same representative stream                                        | Reduced |                 828.0 → 945.1 |                      51.6 → 61.6 |                  87.7 → 119.0 |

Stress-stream long tasks average 3.0 → 1.0 per normal-motion run and 2.0 → 2.0 with reduced motion.
Representative runs produce none in either motion preference or revision. Rendering remains on the
main thread. The producer requests 16 ms intervals but can itself be delayed by main-thread
congestion. These are actual arrival-to-visibility measurements, not a fixed external socket clock.
The rendered rows in the first raw report predate the explicit font/history readiness checks and are
superseded by the settled-stream report above; its codec-only rows are unaffected.

The codec CPU acceptance criterion is met on this device. The issue's requirement for no rendered
latency regression is **not met** in these measurements. The worker implementation is therefore a
draft for local evaluation, not evidence that overall responsiveness is solved. Rendering and worker
reply scheduling need further work before closing #209; a worker pool is not justified by these
results.

## Reproduction

```bash
PLAYWRIGHT_CHROMIUM_EXECUTABLE=/etc/profiles/per-user/avanderbergh/bin/google-chrome \
  Q15_BENCHMARK_CODEC=1 Q15_BENCHMARK_BASE=754b7f123f7389a8eb5f310904896738cbda9c10 \
  Q15_BENCHMARK_RUNS=3 Q15_BENCHMARK_OUTPUT=benchmark-results/209-content-worker.json \
  make ui-benchmark
```

Select only the representative workload with
`Q15_BENCHMARK_WORKLOAD='codec=1&rendered=1&representative=1'`. The harness and workload are copied
into a temporary baseline worktree, using its original codec. See
[benchmark methodology](../README.md#content-worker-measurements-209).

# Codec comparison for #209

Baseline:
[`754b7f123f7389a8eb5f310904896738cbda9c10`](https://github.com/q15co/q15/commit/754b7f123f7389a8eb5f310904896738cbda9c10)
(merged #208). Measured implementation:
[`c869b07454726830c59aae4977587aec27af1dfc`](https://github.com/q15co/q15/commit/c869b07454726830c59aae4977587aec27af1dfc).
The measured tree is `2522e5d0c2634d383a5fc2a79b5fe2dd5ef53703`; `headDirty` is false. The raw
measurements are not committed; the command under
[Codec measurements](../README.md#codec-measurements-209) regenerates them.

Device: Intel(R) Core(TM) i9-10885H CPU @ 2.40GHz, 8 physical cores / 16 logical CPUs; NixOS, linux
6.18.54. Browser: Chrome 154.0.8037.92, headless, 1280 x 900. Node 24.21.0, pnpm 12.8.1 and Vite+
1.0.0. Measured on 2026-10-05, three runs per revision, workload and motion preference. Both
revisions use the production React Compiler from #208 and identical pinned dependencies.
Builds/tests were idle during measurement. The baseline runs before the implementation; thermal/load
variation is not eliminated. These are device measurements, without confidence intervals or timing
thresholds.

The implementation uses native Uint8Array base64 methods and validates decoded objects directly. The
comparison measures these changes together; it does not attribute savings to either change
individually. Both builds run ContentSession on the main thread and use asynchronous WebCrypto, with
no worker RPC. The native path is measured here; fallback compatibility is covered by unit tests,
not timed here.

## Main-thread work

Mean Chrome DevTools Protocol TaskDuration, in milliseconds. Lower is better.

| Workload                 | Motion  | Baseline | Implementation | Change |
| ------------------------ | ------- | -------: | -------------: | -----: |
| Tiny incoming frames     | Normal  |     33.1 |           25.2 | -23.9% |
| Tiny incoming frames     | Reduced |     34.3 |           22.1 | -35.4% |
| Large incoming snapshots | Normal  |    468.3 |           54.2 | -88.4% |
| Large incoming snapshots | Reduced |    478.6 |           47.7 | -90.0% |
| 4,000-turn history       | Normal  |    346.4 |           52.6 | -84.8% |
| 4,000-turn history       | Reduced |    358.9 |           58.4 | -83.7% |
| Large outgoing messages  | Normal  |    216.2 |           61.9 | -71.4% |
| Large outgoing messages  | Reduced |    225.3 |           53.4 | -76.3% |
| Rendered large snapshots | Normal  |   5002.0 |         4795.0 |  -4.1% |
| Rendered large snapshots | Reduced |   2327.0 |         2153.1 |  -7.5% |
| Rendered growing GFM     | Normal  |   2589.5 |         2699.7 |  +4.3% |
| Rendered growing GFM     | Reduced |    904.5 |         1061.6 | +17.4% |

The large codec-only cases reduce measured main-thread work by 71-90%. Each baseline large incoming,
history and outgoing run recorded one task over 50 ms; none of those implementation runs recorded a
task over 50 ms. Tiny frames also use less total task time, but their small durations make
percentage changes less useful.

## Rendered latency

Arrival-to-visible marker latency, in milliseconds. Values pool three runs. The median and p95 use
sorted sample index `floor(p * n)`, capped at `n - 1`. Normal/reduced motion remain separate. Each
large-snapshot row has 144 markers, and each growing-GFM row has 72 markers.

| Workload                 | Motion  | Baseline median / p95 | Implementation median / p95 | Long tasks baseline / implementation |
| ------------------------ | ------- | --------------------: | --------------------------: | -----------------------------------: |
| Rendered large snapshots | Normal  |         142.4 / 315.4 |               128.5 / 220.4 |                              24 / 37 |
| Rendered large snapshots | Reduced |          81.7 / 146.5 |                72.1 / 161.5 |                               8 / 13 |
| Rendered growing GFM     | Normal  |         102.7 / 170.3 |               105.9 / 200.0 |                               5 / 18 |
| Rendered growing GFM     | Reduced |           47.5 / 94.9 |                54.6 / 107.3 |                                2 / 5 |

Rendered results are mixed. Large-snapshot normal-motion p95 improves, while reduced-motion p95 and
both growing-GFM p95 values increase. Growing-GFM main-thread work increases by 4.3% with normal
motion and 17.4% with reduced motion. This run supports landing the codec savings, but does not
establish an overall chat responsiveness improvement or justify introducing a worker. Rendering
remains a separate follow-up; #209 stays open.

## Workloads and timing

- Tiny: 48 sealed incoming frames, no rendering; 37,747 total wire bytes.
- Large: 48 replacement snapshots with a 67,500-byte text prefix, no rendering; 4,362,416 total wire
  bytes.
- History: one sealed 4,000-turn page; 3,168,957 wire bytes.
- Outgoing: 48 messages of 64,000 UTF-8 bytes each, serialized through the production seal path.
- Rendered large: the same 48 large snapshots with 1,000 completed messages.
- Rendered growing GFM: 24 growing text frames with snapshots every tenth frame and deltas
  otherwise, with 1,000 completed messages; 22,996 total wire bytes.

Rendered producers request a frame every 16 ms. Main-thread congestion can delay this producer;
actual arrivals are recorded, and this is not an independently clocked socket load. The sampler
checks markers at requestAnimationFrame opportunities, includes coalesced markers and the existing
40 ms Markdown window, and waits for final reconciliation. This measures a paint opportunity, not
compositor/display completion. The complete historical transcript and fonts settle before counters
start. Key exchange, peer sealing, fixture loading and networking are excluded.

CDP TaskDuration aggregates browser main-thread work, including layout/style and harness work. The
raw `processing` values measure arrival-to-codec-completion including async WebCrypto waits and
serialized queueing, rather than CPU time. Long tasks come from PerformanceObserver (>50 ms). CDP
also includes the trailing 100 ms observer drain. Harness setup and inspection overhead apply to
both revisions. See [the harness documentation](../README.md) for details.

## Reproduce

Check out the measured implementation, then run from the repository root:

```bash
make project-setup
Q15_BENCHMARK_CODEC=1 \
  Q15_BENCHMARK_BASE=754b7f123f7389a8eb5f310904896738cbda9c10 \
  Q15_BENCHMARK_RUNS=3 \
  Q15_BENCHMARK_OUTPUT=benchmark-results/209-codec.json \
  PLAYWRIGHT_CHROMIUM_EXECUTABLE=/etc/profiles/per-user/avanderbergh/bin/google-chrome \
  make ui-benchmark
```

Use the Chrome/Chromium path available on your device. Avoid concurrent builds/tests. The runner
creates and removes a temporary baseline worktree, copies the same harness into it and measures both
production bundles. The implementation commit remains reachable in PR #212 history.

import { chromium } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import {
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { cpus, release, tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { gzipSync } from "node:zlib";

import { TestSealer } from "../e2e/seal.ts";
import { parseClientFrame } from "../src/domain/protocol.ts";
import { frame } from "../src/infrastructure/envelope.ts";
import { growingAnswer, loadedHistory } from "../src/testing/streaming.ts";
import { baselineAdapter } from "./baseline.ts";

const ui = resolve(import.meta.dirname, "..");
const repository = resolve(ui, "../../..");
const pnpm = join(repository, ".tools/bin/pnpm");
const base = process.env.Q15_BENCHMARK_BASE;
const runs = Number(process.env.Q15_BENCHMARK_RUNS ?? "3");
const output = resolve(
  process.env.Q15_BENCHMARK_OUTPUT ?? join(ui, "benchmark-results/results.json"),
);
const browser = await chromium.launch(
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE === undefined
    ? {}
    : { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE },
);
const results: unknown[] = [];
const headTree = execFileSync("git", ["write-tree"], { cwd: repository, encoding: "utf8" }).trim();
const headDirty =
  execFileSync("git", ["status", "--porcelain", "--untracked-files=no"], {
    cwd: repository,
    encoding: "utf8",
  }).trim() !== "";

async function measureRun(
  revision: string,
  motion: "no-preference" | "reduce",
  workload: string,
  run: number,
  port: number,
) {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 900 },
    reducedMotion: motion,
  });
  const errors: string[] = [];
  const failure = new Promise<never>((_resolve, reject) => {
    page.on("pageerror", (error) => {
      errors.push(error.message);
      reject(error);
    });
  });
  let historyPeer: TestSealer | undefined;
  await page.route("**/api/turns?**", async (route) => {
    const peer = historyPeer;
    if (peer === undefined) throw new Error("Missing history peer");
    const params = new URL(route.request().url()).searchParams;
    const before = params.get("after_seq") ?? "0";
    const limit = Number(params.get("limit"));
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 500)
      throw new Error("Invalid history limit");
    const history = loadedHistory(before === "0" ? 4000 : 2000);
    const available = history.turns.filter(
      (turn) => before === "0" || Number(turn.seq) < Number(before),
    );
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify(
        peer.seal(
          frame(
            "history",
            {
              ...history,
              turns: available.slice(0, limit),
              has_more: available.length > limit,
            },
            peer.channelID,
          ),
        ),
      ),
    });
  });
  await page.route("**/benchmark/setup?*", async (route) => {
    const hello = parseClientFrame(route.request().postData() ?? "");
    if (hello.type !== "hello") throw new Error("Invalid codec offer");
    const peer = new TestSealer(hello.payload);
    historyPeer = peer;
    const params = new URL(route.request().url()).searchParams;
    const msg = { turn: "10001", ordinal: -1 };
    const answer = growingAnswer(30);
    let accumulated = "";
    const frames =
      params.has("history") || params.has("outgoing") || params.has("startup")
        ? []
        : Array.from({ length: params.has("representative") ? 24 : 48 }, (_, index) => {
            if (params.has("representative")) {
              const chunk =
                answer.slice(
                  Math.floor((answer.length * index) / 24),
                  Math.floor((answer.length * (index + 1)) / 24),
                ) + `\n\nframe-${index}.\n\n`;
              accumulated += chunk;
              const snapshot = index % 10 === 0;
              return JSON.stringify(
                peer.seal(
                  frame(snapshot ? "snapshot" : "delta", {
                    msg,
                    seq: String(index + 1),
                    kind: "text",
                    text: snapshot ? accumulated : chunk,
                  }),
                ),
              );
            }
            const text =
              (params.has("small") ? "Small content" : "Plain content. ".repeat(4500)) +
              Array.from({ length: index + 1 }, (_unused, marker) => `\n\nframe-${marker}.`).join(
                "",
              );
            accumulated = text;
            return JSON.stringify(
              peer.seal(frame("snapshot", { msg, seq: String(index + 1), kind: "text", text })),
            );
          });
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        key: JSON.stringify(peer.key),
        frames,
        history: JSON.stringify(peer.seal(frame("history", loadedHistory(4000), peer.channelID))),
        paging: JSON.stringify(
          peer.seal(
            frame(
              "history",
              {
                ...loadedHistory(2000),
                turns: loadedHistory(2000).turns.slice(1000, 1250),
              },
              peer.channelID,
            ),
          ),
        ),
        final: JSON.stringify(
          peer.seal(frame("msg.final", { msg, status: "aborted", full_text: accumulated })),
        ),
      }),
    });
  });
  await Promise.race([
    failure,
    page.goto(
      `http://127.0.0.1:${port}/${workload.includes("codec") ? "codec.html" : ""}?${workload}`,
    ),
  ]);
  await Promise.race([
    failure,
    page.waitForSelector('html[data-benchmark-ready="true"]', {
      state: "attached",
      timeout: 120_000,
    }),
  ]);
  const cdp = await page.context().newCDPSession(page);
  const trace = process.env.Q15_BENCHMARK_TRACE === "1" && run === 0;
  if (trace)
    await cdp.send("Tracing.start", {
      categories:
        "devtools.timeline,blink.user_timing,v8.execute,disabled-by-default-v8.cpu_profiler",
      transferMode: "ReturnAsStream",
    });
  await cdp.send("Performance.enable");
  const before = await cdp.send("Performance.getMetrics");
  await page.evaluate(() => document.dispatchEvent(new Event("benchmark-start")));
  const result = await Promise.race([
    failure,
    page.waitForSelector("[data-benchmark-result]", {
      state: "attached",
      timeout: 240_000,
    }),
  ]);
  const after = await cdp.send("Performance.getMetrics");
  let tracePath: string | undefined;
  if (trace) {
    const completed = new Promise<string>((done) => {
      cdp.once("Tracing.tracingComplete", (event) => {
        if (event.stream === undefined) throw new Error("Missing trace stream");
        done(event.stream);
      });
    });
    await cdp.send("Tracing.end");
    const handle = await completed;
    let data = "";
    for (;;) {
      const chunk = await cdp.send("IO.read", { handle });
      data +=
        chunk.base64Encoded === true ? Buffer.from(chunk.data, "base64").toString() : chunk.data;
      if (chunk.eof) break;
    }
    await cdp.send("IO.close", { handle });
    tracePath = `${revision.slice(0, 8)}-${motion}-${workload.replaceAll(/[^a-z0-9]/gu, "-")}.json.gz`;
    mkdirSync(join(dirname(output), "traces"), { recursive: true });
    writeFileSync(join(dirname(output), "traces", tracePath), gzipSync(data));
  }
  const data = await result.getAttribute("data-benchmark-result");
  if (data === null || errors.length > 0)
    throw new Error(`Invalid benchmark result: ${errors.join(", ")}`);
  const metrics: Record<string, number> = {};
  for (const metric of after.metrics) {
    if (
      ["TaskDuration", "ScriptDuration", "LayoutDuration", "RecalcStyleDuration"].includes(
        metric.name,
      )
    ) {
      metrics[`${metric.name}Ms`] =
        1000 *
        (metric.value - (before.metrics.find((item) => item.name === metric.name)?.value ?? 0));
    }
  }
  const workloadResult: unknown = JSON.parse(data);
  if (run >= 0)
    results.push({
      revision,
      motion,
      workload,
      run,
      metrics,
      trace: tracePath,
      result: workloadResult,
    });
  process.stdout.write(
    `${revision.slice(0, 8)} ${motion} ${workload} run ${run + 1}: ${(metrics.TaskDurationMs ?? 0).toFixed(1)} ms main thread\n`,
  );
  await page.close();
}

async function preview(directory: string, port: number) {
  const server = spawn(
    pnpm,
    [
      "exec",
      "vp",
      "preview",
      "benchmark",
      "--config",
      "benchmark/vite.config.ts",
      "--port",
      String(port),
      "--strictPort",
      "--host",
      "127.0.0.1",
    ],
    { cwd: directory, stdio: "ignore" },
  );
  try {
    for (let attempt = 0; ; attempt++) {
      try {
        const response = await fetch(`http://127.0.0.1:${port}`);
        if (response.ok) break;
      } catch {
        /* The preview process may still be starting. */
      }
      if (attempt >= 100 || server.exitCode !== null)
        throw new Error("Benchmark preview failed to start");
      await new Promise<void>((done) => {
        setTimeout(done, 100);
      });
    }
    return server;
  } catch (error) {
    server.kill();
    throw error;
  }
}

async function measure(variants: readonly { directory: string; revision: string; port: number }[]) {
  const servers = [];
  try {
    for (const variant of variants) servers.push(await preview(variant.directory, variant.port));
    for (const motion of ["no-preference", "reduce"] satisfies ("no-preference" | "reduce")[]) {
      const defaults =
        process.env.Q15_BENCHMARK_CODEC === "1"
          ? [
              "codec=1&small=1",
              "codec=1&large=1",
              "codec=1&history=1",
              "codec=1&outgoing=1",
              "codec=1&rendered=1",
              "codec=1&rendered=1&representative=1",
              "codec=1&rendered=1&representative=1&paging=1",
              "codec=1&startup=1",
            ]
          : [
              "history=100",
              "history=1000",
              "history=0&large=1",
              ...(motion === "no-preference" ? ["history=1000&staticAxes=1"] : []),
            ];
      const workloads = process.env.Q15_BENCHMARK_WORKLOAD?.split(",") ?? defaults;
      for (const workload of workloads) {
        await repetitions(variants, motion, workload);
      }
    }
  } finally {
    for (const server of servers) {
      const stopped = new Promise<void>((done) => {
        server.once("exit", () => done());
      });
      server.kill();
      await stopped;
    }
  }
}

async function repetitions(
  variants: readonly { directory: string; revision: string; port: number }[],
  motion: "no-preference" | "reduce",
  workload: string,
) {
  for (let run = -1; run < runs; run++) {
    const ordered = run % 2 === 0 ? variants : variants.toReversed();
    for (const variant of ordered)
      await measureRun(variant.revision, motion, workload, run, variant.port);
  }
}

try {
  const revision = execFileSync("git", ["rev-parse", "HEAD"], {
    cwd: repository,
    encoding: "utf8",
  }).trim();
  const variants = [
    { directory: ui, revision: headDirty ? `staged:${headTree}` : revision, port: 4195 },
  ];
  if (base === undefined) await measure(variants);
  else {
    const checkout = mkdtempSync(join(tmpdir(), "q15-streaming-base-"));
    const baseRevision = execFileSync("git", ["rev-parse", base], {
      cwd: repository,
      encoding: "utf8",
    }).trim();
    try {
      execFileSync("git", ["worktree", "add", "--detach", checkout, baseRevision], {
        cwd: repository,
        stdio: "ignore",
      });
      const baselineUI = join(checkout, "systems/web/ui");
      const adapterPath = join(baselineUI, "benchmark/codec-adapter.ts");
      const selectedAdapter = existsSync(adapterPath)
        ? readFileSync(adapterPath, "utf8")
        : undefined;
      cpSync(join(ui, "benchmark"), join(baselineUI, "benchmark"), { recursive: true });
      if (process.env.Q15_BENCHMARK_CODEC === "1")
        writeFileSync(adapterPath, baselineAdapter(selectedAdapter));
      cpSync(join(ui, "src/testing/streaming.ts"), join(baselineUI, "src/testing/streaming.ts"));
      symlinkSync(join(ui, "node_modules"), join(baselineUI, "node_modules"), "dir");
      execFileSync("make", ["ui-benchmark-base-build", `BENCHMARK_UI_DIR=${baselineUI}`], {
        cwd: repository,
        stdio: "inherit",
      });
      variants.unshift({ directory: baselineUI, revision: baseRevision, port: 4196 });
      await measure(variants);
    } finally {
      execFileSync("git", ["worktree", "remove", "--force", checkout], { cwd: repository });
      rmSync(checkout, { recursive: true, force: true });
    }
  }
  mkdirSync(dirname(output), { recursive: true });
  writeFileSync(
    output,
    JSON.stringify(
      {
        cpu: cpus()[0]?.model,
        os: `${process.platform} ${release()}`,
        browser: browser.version(),
        runs,
        ordering:
          "alternating baseline/head per pair after one unrecorded warmup per variant/workload/motion",
        headTree,
        headDirty,
        results,
      },
      null,
      2,
    ) + "\n",
  );
  process.stdout.write(`Results: ${output}\n`);
} finally {
  await browser.close();
}

import { chromium } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { cpus, release, tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";

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
) {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 900 },
    reducedMotion: motion,
  });
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(`http://127.0.0.1:4195/?${workload}`);
  await page.waitForSelector('html[data-benchmark-ready="true"]', { timeout: 120_000 });
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Performance.enable");
  const before = await cdp.send("Performance.getMetrics");
  await page.evaluate(() => document.dispatchEvent(new Event("benchmark-start")));
  const result = await page.waitForSelector("[data-benchmark-result]", {
    state: "attached",
    timeout: 240_000,
  });
  const after = await cdp.send("Performance.getMetrics");
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
  results.push({ revision, motion, workload, run, metrics, result: workloadResult });
  process.stdout.write(
    `${revision.slice(0, 8)} ${motion} ${workload} run ${run + 1}: ${(metrics.TaskDurationMs ?? 0).toFixed(1)} ms main thread\n`,
  );
  await page.close();
}

async function measure(directory: string, revision: string) {
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
      "4195",
      "--strictPort",
      "--host",
      "127.0.0.1",
    ],
    { cwd: directory, stdio: "ignore" },
  );
  try {
    for (let attempt = 0; ; attempt++) {
      try {
        const response = await fetch("http://127.0.0.1:4195");
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
    for (const motion of ["no-preference", "reduce"] satisfies ("no-preference" | "reduce")[]) {
      const workloads = [
        "history=100",
        "history=1000",
        "history=0&large=1",
        ...(motion === "no-preference" ? ["history=1000&staticAxes=1"] : []),
      ];
      for (const workload of workloads) {
        for (let run = 0; run < runs; run++) {
          await measureRun(revision, motion, workload, run);
        }
      }
    }
  } finally {
    server.kill();
    await new Promise<void>((done) => {
      server.once("exit", () => done());
    });
  }
}

try {
  if (base !== undefined) {
    const checkout = mkdtempSync(join(tmpdir(), "q15-streaming-base-"));
    const revision = execFileSync("git", ["rev-parse", base], {
      cwd: repository,
      encoding: "utf8",
    }).trim();
    try {
      execFileSync("git", ["worktree", "add", "--detach", checkout, revision], {
        cwd: repository,
        stdio: "ignore",
      });
      const baselineUI = join(checkout, "systems/web/ui");
      cpSync(join(ui, "benchmark"), join(baselineUI, "benchmark"), { recursive: true });
      cpSync(join(ui, "src/testing/streaming.ts"), join(baselineUI, "src/testing/streaming.ts"));
      symlinkSync(join(ui, "node_modules"), join(baselineUI, "node_modules"), "dir");
      execFileSync("make", ["ui-benchmark-base-build", `BENCHMARK_UI_DIR=${baselineUI}`], {
        cwd: repository,
        stdio: "inherit",
      });
      await measure(baselineUI, revision);
    } finally {
      execFileSync("git", ["worktree", "remove", "--force", checkout], { cwd: repository });
      rmSync(checkout, { recursive: true, force: true });
    }
  }
  const revision = execFileSync("git", ["rev-parse", "HEAD"], {
    cwd: repository,
    encoding: "utf8",
  }).trim();
  await measure(ui, headDirty ? `staged:${headTree}` : revision);
  mkdirSync(dirname(output), { recursive: true });
  writeFileSync(
    output,
    JSON.stringify(
      {
        cpu: cpus()[0]?.model,
        os: `${process.platform} ${release()}`,
        browser: browser.version(),
        runs,
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

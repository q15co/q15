import { spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vite-plus/test";

// Exercise the actual pinned Oxlint configuration, including override precedence,
// re-exports and JS plugin loading. Fixtures exist only for the subprocess lifetime.
function probe(cases: Record<string, string>) {
  const paths = Object.keys(cases).map((path) => resolve(path));
  try {
    for (const [path, source] of Object.entries(cases)) {
      mkdirSync(resolve(path, ".."), { recursive: true });
      writeFileSync(path, source);
    }
    return spawnSync(resolve("node_modules/.bin/vp"), ["lint", "--format", "unix", ...paths], {
      encoding: "utf8",
      timeout: 30_000,
      env: { ...process.env, NO_COLOR: "1" },
    });
  } finally {
    for (const path of paths) rmSync(path, { force: true });
  }
}

function formatFixture(path: string, ...args: string[]) {
  return spawnSync(resolve("node_modules/.bin/vp"), ["fmt", ...args, path], {
    encoding: "utf8",
    timeout: 30_000,
    env: { ...process.env, NO_COLOR: "1" },
  });
}

describe("Oxlint architecture guards", () => {
  it("rejects forbidden dependencies, including type imports, re-exports and dynamic imports", () => {
    const cases = {
      "src/domain/architecture-probe-ui.ts": 'export { App } from "../app";',
      "src/domain/architecture-probe-react.ts": 'export { useState } from "react";',
      "src/domain/architecture-probe-type.ts":
        'export type { ChatStore } from "../application/chat-store";',
      "src/domain/architecture-probe-inline.ts":
        'export type Store = import("../application/chat-store").ChatStore;',
      "src/domain/architecture-probe-socket.ts":
        'export { SocketTransport } from "../infrastructure/transport";',
      "src/domain/architecture-probe-dynamic.ts":
        'export const load = () => import("../infrastructure/transport");',
      "src/domain/architecture-probe-computed.ts":
        "export const load = (path: string) => import(path);",
      "src/domain/architecture-probe-traversal.ts": 'export { App } from "./../app";',
      "src/domain/architecture-probe-alias.ts": 'export { App } from "@q15/app";',
      "src/domain/architecture-probe-package.ts": 'export { readFileSync } from "node:fs";',
      "src/application/architecture-probe-display.ts": 'export { App } from "../app";',
      "src/application/architecture-probe-adapter.ts":
        'export { SocketTransport } from "../infrastructure/transport";',
      "src/components/architecture-probe-adapter.ts":
        'export { SocketTransport } from "../infrastructure/transport";',
      "src/architecture-probe-root.ts":
        'export { SocketTransport } from "./infrastructure/transport";',
      "src/shared/architecture-probe-domain.ts": 'export { reduceFrame } from "../domain/chat";',
      "src/domain/architecture-probe-test.ts": 'export { required } from "../testing/required";',
      "src/components/architecture-probe-worker.ts":
        'export { ContentEngine } from "../infrastructure/content-engine";',
      "src/infrastructure/architecture-probe-worker.ts":
        'export { default } from "virtual:q15-content-worker";',
      "src/infrastructure/content-engine-probe.ts": 'export { requestProof } from "./proof";',
      "src/infrastructure/transport-probe.ts": 'export { ContentSession } from "./seal";',
      "src/infrastructure/history-probe.ts": 'export { ContentEngine } from "./content-engine.ts";',
      "worker/architecture-probe-ui.ts": 'export { App } from "../src/app";',
      "build/architecture-probe-domain.ts": 'export { reduceFrame } from "../src/domain/chat";',
    };
    const result = probe(cases);
    expect(result.error).toBeUndefined();
    expect(result.status).toBe(1);
    const output = result.stdout + result.stderr;
    for (const path of Object.keys(cases)) {
      const rule = path.endsWith("inline.ts")
        ? "consistent-type-imports"
        : path.endsWith("computed.ts") || path.endsWith("traversal.ts")
          ? "literal-imports"
          : "no-restricted-imports";
      const diagnostics = output.split("\n").filter((line) => line.startsWith(`${path}:`));
      expect(diagnostics.join("\n")).toContain(rule);
    }
  });

  it("rejects clocks, randomness, browser effects and input mutation in pure modules", () => {
    const result = probe({
      "src/domain/architecture-probe-effects.ts": `
        export const now = () => Date.now();
        export const random = () => Math.random();
        export const load = () => fetch("/api/turns");
        export const storage = () => globalThis.localStorage;
        export const later = () => setTimeout(() => {}, 1);
        export function mutate(value: { text: string }) { value.text = "changed"; }
      `,
    });
    expect(result.error).toBeUndefined();
    expect(result.status).toBe(1);
    const output = result.stdout + result.stderr;
    expect(output).toContain("no-restricted-globals");
    expect(output).toContain("no-restricted-properties");
    expect(output).toContain("no-param-reassign");
  });

  it("accepts inward dependencies and deterministic transformations", () => {
    const result = probe({
      "src/domain/architecture-probe-allowed.ts": `
        import { isRecord } from "../shared/type-guards";
        export const object = (value: unknown) => isRecord(value);
      `,
      "src/application/architecture-probe-allowed.ts":
        'export { reduceFrame } from "../domain/chat";',
      "src/components/architecture-probe-allowed.ts":
        'export type { ChatState } from "../domain/chat";',
      "worker/architecture-probe-allowed.ts":
        'export type { ShellManifest } from "../src/shared/shell-manifest";',
    });
    expect(result.error).toBeUndefined();
    expect(result.status).toBe(0);
    expect(result.stdout).toBe("");
  });

  it("rejects direct browser I/O in application orchestration", () => {
    const path = "src/application/architecture-probe-effects.ts";
    const result = probe({ [path]: 'export const load = () => fetch("/api/turns");' });
    expect(result.error).toBeUndefined();
    expect(result.status).toBe(1);
    expect(result.stdout).toContain(`${path}:`);
    expect(result.stdout).toContain("no-restricted-globals");
  });

  it.each(["ts", "test.ts"])("retains type-safety rules in %s files", (extension) => {
    const cases = {
      assertion: {
        source: "export const text = (value: unknown) => value as string;",
        rule: "consistent-type-assertions",
      },
      any: {
        source: "export const echo = (value: any) => value;",
        rule: "no-explicit-any",
      },
      nonnull: {
        source: "export const text = (value: string | undefined) => value!;",
        rule: "no-non-null-assertion",
      },
      unsafe: {
        source: "export const text: string = JSON.parse('\"value\"');",
        rule: "no-unsafe-assignment",
      },
      promise: {
        source: 'export function run() { Promise.resolve("ready"); }',
        rule: "no-floating-promises",
      },
    };
    const path = (name: string) => `src/domain/architecture-probe-${name}.${extension}`;
    const result = probe(
      Object.fromEntries(Object.entries(cases).map(([name, value]) => [path(name), value.source])),
    );
    expect(result.error).toBeUndefined();
    expect(result.status).toBe(1);
    const output = result.stdout + result.stderr;
    for (const [name, value] of Object.entries(cases)) {
      const diagnostics = output.split("\n").filter((line) => line.startsWith(`${path(name)}:`));
      expect(diagnostics.join("\n")).toContain(value.rule);
    }
  });

  it("rejects cycles using the native import plugin", () => {
    const paths = [
      "src/domain/architecture-probe-first.ts",
      "src/domain/architecture-probe-second.ts",
    ];
    const result = probe({
      "src/domain/architecture-probe-first.ts": `
        import { second } from "./architecture-probe-second";
        export function first(): number { return second(); }
      `,
      "src/domain/architecture-probe-second.ts": `
        import { first } from "./architecture-probe-first";
        export function second(): number { return first(); }
      `,
    });
    expect(result.error).toBeUndefined();
    expect(result.status).toBe(1);
    for (const path of paths) {
      const diagnostics = result.stdout.split("\n").filter((line) => line.startsWith(`${path}:`));
      expect(diagnostics.join("\n")).toContain("import(no-cycle)");
    }
  });

  it("sorts imports automatically and preserves CSS side-effect order", () => {
    const path = resolve("lint/architecture-probe-sorting.ts");
    const run = (...args: string[]) =>
      spawnSync(resolve("node_modules/.bin/vp"), ["fmt", ...args, path], {
        encoding: "utf8",
        timeout: 30_000,
        env: { ...process.env, NO_COLOR: "1" },
      });
    try {
      writeFileSync(
        path,
        `import { zeta } from "../src/zeta";
import { alpha } from "../src/alpha";
import { readFileSync } from "node:fs";
import type { ChatState } from "../src/domain/chat";
import "./z-reset.css";
import "./a-theme.css";
`,
      );
      expect(run("--check").status).toBe(1);
      const formatted = run();
      expect(formatted.error).toBeUndefined();
      expect(formatted.status).toBe(0);
      const source = readFileSync(path, "utf8");
      expect(source.indexOf("node:fs")).toBeLessThan(source.indexOf("../src/domain/chat"));
      expect(source.indexOf("../src/alpha")).toBeLessThan(source.indexOf("../src/zeta"));
      expect(source.indexOf("./z-reset.css")).toBeLessThan(source.indexOf("./a-theme.css"));
      expect(run("--check").status).toBe(0);
    } finally {
      rmSync(path, { force: true });
    }
  });

  it("formats canonical and browser JSON fixtures identically", () => {
    const canonical = resolve(
      "../../../libs/chat-contract/browser/protocol/testdata/architecture-probe-format.json",
    );
    const copy = resolve("src/fixtures/protocol/architecture-probe-format.json");
    const paths = [canonical, copy];
    const source = '{"turns":[],"head_seq":"0","has_more":false}\n';
    try {
      for (const path of paths) {
        writeFileSync(path, source);
        expect(formatFixture(path, "--check").status).toBe(1);
        const formatted = formatFixture(path);
        expect(formatted.error).toBeUndefined();
        expect(formatted.status).toBe(0);
        expect(formatFixture(path, "--check").status).toBe(0);
      }
      expect(readFileSync(canonical, "utf8")).toBe(readFileSync(copy, "utf8"));
      const value: unknown = JSON.parse(readFileSync(canonical, "utf8"));
      expect(value).toEqual({ turns: [], head_seq: "0", has_more: false });
    } finally {
      for (const path of paths) rmSync(path, { force: true });
    }
  });
});

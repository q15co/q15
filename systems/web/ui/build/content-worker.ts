import type { Plugin } from "vite-plus";

import { resolve } from "node:path";
import { build } from "vite-plus";

export async function compileContentWorker(root: string) {
  const result = await build({
    root,
    configFile: false,
    publicDir: false,
    logLevel: "silent",
    build: {
      write: false,
      rolldownOptions: {
        input: resolve(root, "worker/content.ts"),
        output: { format: "iife" },
      },
    },
  });
  if (Array.isArray(result) || !("output" in result) || result.output.length !== 1)
    throw new Error("Expected one self-contained content worker.");
  const entry = result.output.at(0);
  if (!entry || entry.type !== "chunk") throw new Error("Missing content worker script.");
  return entry;
}

export function contentWorker(): Plugin {
  const root = resolve(import.meta.dirname, "..");
  let compiled: ReturnType<typeof compileContentWorker> | undefined;
  return {
    name: "q15-content-worker",
    resolveId: (id) => (id === "virtual:q15-content-worker" ? "\0q15-content-worker" : undefined),
    async load(id) {
      if (id !== "\0q15-content-worker") return null;
      const entry = await (compiled ??= compileContentWorker(root));
      for (const module of entry.moduleIds) this.addWatchFile(module);
      return "export default " + JSON.stringify(entry.code) + ";";
    },
    watchChange() {
      compiled = undefined;
    },
  };
}

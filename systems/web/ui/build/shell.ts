import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { build } from "vite-plus";
import type { Plugin, ResolvedConfig } from "vite-plus";
import type { ShellManifest } from "../src/shell-manifest";

export async function buildWorker(root: string, paths: string[], version: string) {
  const manifest = { paths, version } satisfies ShellManifest;
  const result = await build({
    root,
    configFile: false,
    publicDir: false,
    logLevel: "silent",
    define: { __SHELL__: JSON.stringify(manifest) },
    build: {
      write: false,
      rolldownOptions: {
        input: resolve(root, "worker/sw.ts"),
        output: { format: "iife", entryFileNames: "sw.js" },
      },
    },
  });
  if (Array.isArray(result) || !("output" in result))
    throw new Error("Expected one service worker bundle");
  const worker = result.output[0];
  if (worker?.type !== "chunk" || result.output.length !== 1)
    throw new Error("The service worker must compile to one self-contained script");
  return worker;
}

export function shell(): Plugin {
  let config: ResolvedConfig;
  return {
    name: "q15-shell",
    apply: "build",
    enforce: "post",
    configResolved(resolved) {
      config = resolved;
    },
    generateBundle: {
      // Vite finishes HTML and dynamic-import preload rewriting in this hook.
      order: "post",
      async handler(_, bundle) {
        const paths = [
          "/index.html",
          "/manifest.webmanifest",
          "/icon.svg",
          "/icon-192.png",
          "/icon-512.png",
          ...Object.keys(bundle)
            .filter((path) => path.startsWith("assets/"))
            .map((path) => `/${path}`),
        ].sort();
        const digest = createHash("sha256");
        for (const path of paths) {
          digest.update(path);
          const entry = bundle[path.slice(1)];
          if (entry) digest.update(entry.type === "chunk" ? entry.code : entry.source);
          else digest.update(readFileSync(resolve(config.publicDir, path.slice(1))));
        }
        const version = digest.digest("hex").slice(0, 16);
        const worker = await buildWorker(config.root, paths, version);
        worker.moduleIds.forEach((path) => this.addWatchFile(path));
        this.emitFile({ type: "asset", fileName: "sw.js", source: worker.code });
        this.emitFile({
          type: "asset",
          fileName: "shell-manifest.json",
          source: `${JSON.stringify({ version, paths }, null, 2)}\n`,
        });
        this.emitFile({ type: "asset", fileName: ".gitkeep", source: "" });
      },
    },
  };
}

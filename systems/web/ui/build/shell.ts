import type { DefaultTreeAdapterMap } from "parse5";
import type { Plugin, ResolvedConfig } from "vite-plus";

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { parse, serialize } from "parse5";
import { build } from "vite-plus";

import type { ShellManifest } from "../src/shared/shell-manifest";

function inlineShell(source: string, script: string, style: string) {
  const tree = parse(source);
  const nodes: DefaultTreeAdapterMap["node"][] = [tree];
  let scripts = 0;
  let styles = 0;
  for (const node of nodes) {
    if (!("childNodes" in node)) continue;
    nodes.push(...node.childNodes);
    if (!("tagName" in node)) continue;
    const isScript = node.tagName === "script" && node.attrs.some((attr) => attr.name === "src");
    const isStyle =
      node.tagName === "link" &&
      node.attrs.some((attr) => attr.name === "rel" && attr.value === "stylesheet");
    if (!isScript && !isStyle) continue;
    node.nodeName = node.tagName = isScript ? "script" : "style";
    node.attrs = [{ name: "nonce", value: "__Q15_NONCE__" }];
    if (isScript) {
      node.attrs.push({ name: "type", value: "module" });
      scripts++;
    } else styles++;
    node.childNodes = [
      {
        nodeName: "#text",
        parentNode: node,
        value: isScript
          ? script.replaceAll(/<\/script/giu, "\\u003c/script")
          : style.replaceAll(/<\/style/giu, "\\3c /style"),
      },
    ];
  }
  if (scripts !== 1 || styles !== 1)
    throw new Error("Expected one module and stylesheet in the UI shell");
  return serialize(tree);
}

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
  if (result.output.length !== 1)
    throw new Error("The service worker must compile to one self-contained script");
  return worker;
}

export function shell(): Plugin {
  let config: ResolvedConfig;
  return {
    name: "q15-shell",
    apply: "build",
    enforce: "post",
    config() {
      return {
        build: {
          assetsInlineLimit: Number.MAX_SAFE_INTEGER,
          rolldownOptions: { output: { codeSplitting: false } },
        },
      };
    },
    configResolved(resolved) {
      config = resolved;
    },
    generateBundle: {
      // Vite finishes HTML and dynamic-import preload rewriting in this hook.
      order: "post",
      async handler(_, bundle) {
        const html = bundle["index.html"];
        const scripts = Object.values(bundle).filter((entry) => entry.type === "chunk");
        const styles = Object.values(bundle).filter(
          (entry) => entry.type === "asset" && entry.fileName.endsWith(".css"),
        );
        const script = scripts[0];
        const style = styles[0];
        if (
          !html ||
          html.type !== "asset" ||
          typeof html.source !== "string" ||
          !script ||
          scripts.length !== 1 ||
          !style ||
          style.type !== "asset" ||
          typeof style.source !== "string" ||
          styles.length !== 1
        )
          throw new Error("Expected a self-contained UI shell");
        html.source = inlineShell(html.source, script.code, style.source);
        delete bundle[script.fileName];
        delete bundle[style.fileName];
        const paths = [
          "/index.html",
          "/manifest.webmanifest",
          "/icon.svg",
          "/icon-192.png",
          "/icon-512.png",
          ...Object.keys(bundle)
            .filter((path) => path.startsWith("assets/"))
            .map((path) => `/${path}`),
        ].toSorted();
        const digest = createHash("sha256");
        for (const path of paths) {
          digest.update(path);
          const entry = bundle[path.slice(1)];
          if (entry?.type === "asset") digest.update(entry.source);
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

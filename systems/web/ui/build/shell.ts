import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import type { Plugin } from "vite-plus";

export function workerSource(paths: string[], version: string) {
  // Only the compiler's static shell allow-list can enter Cache Storage.
  return `const SHELL = ${JSON.stringify(paths)};
const CACHE = "q15-shell-${version}";
self.addEventListener("install", event => {
  event.waitUntil(caches.open(CACHE).then(cache => cache.addAll(SHELL)));
});
self.addEventListener("activate", event => {
  event.waitUntil(caches.keys().then(keys => Promise.all(keys.filter(key => key.startsWith("q15-shell-") && key !== CACHE).map(key => caches.delete(key)))));
});
self.addEventListener("fetch", event => {
  const request = event.request;
  const url = new URL(request.url);
  const path = request.mode === "navigate" && url.pathname === "/" ? "/index.html" : url.pathname;
  if (request.method !== "GET" || url.origin !== self.location.origin || url.search || !SHELL.includes(path)) return;
  if (path === "/index.html") {
    event.respondWith(fetch(request).catch(() => caches.open(CACHE).then(cache => cache.match("/index.html"))));
  } else {
    event.respondWith(caches.open(CACHE).then(async cache => (await cache.match(url.pathname)) || fetch(request)));
  }
});
`;
}

export function shell(): Plugin {
  return {
    name: "q15-shell",
    generateBundle(_, bundle) {
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
        else if (path !== "/index.html")
          digest.update(readFileSync(resolve("public", path.slice(1))));
      }
      const version = digest.digest("hex").slice(0, 16);
      this.emitFile({ type: "asset", fileName: "sw.js", source: workerSource(paths, version) });
      this.emitFile({
        type: "asset",
        fileName: "shell-manifest.json",
        source: `${JSON.stringify({ version, paths }, null, 2)}\n`,
      });
      this.emitFile({ type: "asset", fileName: ".gitkeep", source: "" });
    },
  };
}

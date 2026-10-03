import type { ShellManifest } from "../src/shared/shell-manifest";

import { requestProof } from "../src/infrastructure/proof";

declare const self: ServiceWorkerGlobalScope;
// Vite injects the completed build's exact shell allow-list and content digest.
declare const __SHELL__: ShellManifest;

const manifest = __SHELL__;
const shell = new Set(manifest.paths);
const cacheName = `q15-shell-${manifest.version}`;

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(cacheName).then((cache) =>
      Promise.all(
        manifest.paths.map(async (path) => {
          const response = await signedFetch(new Request(new URL(path, self.location.origin)));
          if (!response.ok) throw new Error("Shell authentication failed.");
          await cache.put(path, response);
        }),
      ),
    ),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter((key) => key.startsWith("q15-shell-") && key !== cacheName)
            .map((key) => caches.delete(key)),
        ),
      ),
  );
});

async function signedFetch(request: Request) {
  const url = new URL(request.url);
  const headers = new Headers(request.headers);
  headers.set(
    "Q15-Proof",
    await requestProof(request.method, `${url.pathname}${url.search}`, url.origin),
  );
  return fetch(new Request(request, { headers, cache: "no-store" }));
}

async function navigation(request: Request) {
  try {
    return await signedFetch(request);
  } catch {
    const cache = await caches.open(cacheName);
    return (await cache.match("/index.html")) ?? Response.error();
  }
}

async function asset(request: Request, path: string) {
  const cache = await caches.open(cacheName);
  return (await cache.match(path)) ?? signedFetch(request);
}

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const url = new URL(request.url);
  const path = request.mode === "navigate" && url.pathname === "/" ? "/index.html" : url.pathname;
  if (
    request.method !== "GET" ||
    url.origin !== self.location.origin ||
    url.search !== "" ||
    !shell.has(path)
  )
    return;
  event.respondWith(path === "/index.html" ? navigation(request) : asset(request, path));
});

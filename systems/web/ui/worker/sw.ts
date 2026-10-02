export {};

declare const self: ServiceWorkerGlobalScope;
// Vite injects the completed build's exact shell allow-list and content digest.
declare const __SHELL__: { paths: string[]; version: string };

const manifest = __SHELL__;
const shell = new Set(manifest.paths);
const cacheName = `q15-shell-${manifest.version}`;

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(cacheName).then((cache) => cache.addAll(manifest.paths)));
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

async function navigation(request: Request) {
  try {
    return await fetch(request);
  } catch {
    const cache = await caches.open(cacheName);
    return (await cache.match("/index.html")) ?? Response.error();
  }
}

async function asset(request: Request, path: string) {
  const cache = await caches.open(cacheName);
  return (await cache.match(path)) ?? fetch(request);
}

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const url = new URL(request.url);
  const path = request.mode === "navigate" && url.pathname === "/" ? "/index.html" : url.pathname;
  if (
    request.method !== "GET" ||
    url.origin !== self.location.origin ||
    url.search ||
    !shell.has(path)
  )
    return;
  event.respondWith(path === "/index.html" ? navigation(request) : asset(request, path));
});

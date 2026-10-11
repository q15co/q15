import type { ShellManifest } from "../src/shared/shell-manifest";

import { requestProof } from "../src/infrastructure/proof";
import {
  ShareCacheName,
  ShareField,
  ShareFilenameHeader,
  SharePath,
  ShareRejected,
  shareEntryPath,
  shareFits,
} from "../src/shared/share-inbox";

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
      )
      .then(() => self.clients.claim()),
  );
});

async function signedFetch(request: Request) {
  const url = new URL(request.url);
  const headers = new Headers(request.headers);
  headers.set(
    "Q15-Proof",
    await requestProof(request.method, `${url.pathname}${url.search}`, url.origin),
  );
  return fetch(
    new Request(request, {
      headers,
      cache: "no-store",
      credentials: "same-origin",
      mode: "same-origin",
    }),
  );
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

/** The share sheet posts here, so the reader lands in the app with the files already in hand. */
function isSharePost(request: Request, url: URL): boolean {
  return (
    request.method === "POST" && url.origin === self.location.origin && url.pathname === SharePath
  );
}

function shareRedirect(request: Request, target: string): Response {
  return Response.redirect(new URL(target, request.url).href, 303);
}

/** Keeps the shared files in the app's inbox, and answers whether all of them are there. */
async function keepShare(request: Request, receivedAt: number): Promise<boolean> {
  try {
    const form = await request.formData();
    const files = form.getAll(ShareField).filter((value): value is File => value instanceof File);
    if (shareFits(files.map((file) => file.size))) {
      const inbox = await caches.open(ShareCacheName);
      const staged = files.map((file, index) => ({
        path: shareEntryPath(receivedAt, index),
        file,
      }));
      try {
        await Promise.all(
          staged.map(({ path, file }) =>
            inbox.put(
              path,
              new Response(file, {
                headers: { [ShareFilenameHeader]: encodeURIComponent(file.name) },
              }),
            ),
          ),
        );
        return true;
      } catch {
        // Keep the inbox honest: a share is either all there or not kept at all.
        await Promise.all(staged.map(({ path }) => inbox.delete(path)));
        return false;
      }
    }
    return false;
  } catch {
    // A share that cannot be read still lands the reader in the app.
    return false;
  }
}

async function receiveShare(request: Request, receivedAt: number): Promise<Response> {
  // A cross-site post is another page's form submission rather than the share sheet: answer it so no
  // reader ever meets a 404 page, and keep nothing.
  if (request.headers.get("sec-fetch-site") === "cross-site") return shareRedirect(request, "/");
  const kept = await keepShare(request, receivedAt);
  return shareRedirect(request, kept ? "/" : `/?share=${ShareRejected}`);
}

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const url = new URL(request.url);
  if (isSharePost(request, url)) {
    event.respondWith(receiveShare(request, Date.now()));
    return;
  }
  // The app root is the shell, whatever query string the reader arrived with.
  const navigating = request.mode === "navigate" && url.pathname === "/";
  const path = navigating ? "/index.html" : url.pathname;
  if (
    request.method !== "GET" ||
    url.origin !== self.location.origin ||
    (!navigating && url.search !== "") ||
    !shell.has(path)
  )
    return;
  event.respondWith(path === "/index.html" ? navigation(request) : asset(request, path));
});

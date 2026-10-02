// @vitest-environment node
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { runInNewContext } from "node:vm";
import { beforeAll, describe, expect, it, vi } from "vite-plus/test";
import { buildWorker } from "./shell";

const paths = ["/index.html", "/assets/app-abcdef12.js"];
const cacheName = "q15-shell-abcdef12";
let source: string;

interface WorkerEvent {
  request: Request;
  waitUntil: (promise: Promise<unknown>) => void;
  respondWith: (promise: Promise<Response>) => void;
}

function worker() {
  const handlers = new Map<string, (event: WorkerEvent) => void>();
  const cache = {
    addAll: vi.fn<(paths: string[]) => Promise<void>>().mockResolvedValue(undefined),
    match: vi.fn<(path: string) => Promise<Response | undefined>>(
      async (path: string): Promise<Response | undefined> => new Response(`cached ${path}`),
    ),
    put: vi.fn<Cache["put"]>(),
  };
  const caches = {
    open: vi.fn<(name: string) => Promise<typeof cache>>().mockResolvedValue(cache),
    keys: vi
      .fn<() => Promise<string[]>>()
      .mockResolvedValue(["q15-shell-old", cacheName, "other-app-cache"]),
    delete: vi.fn<(name: string) => Promise<boolean>>().mockResolvedValue(true),
  };
  const fetch = vi
    .fn<(request: Request) => Promise<Response>>()
    .mockResolvedValue(new Response("network"));
  runInNewContext(source, {
    self: {
      location: { origin: "https://chat.example" },
      addEventListener: (type: string, handler: (event: WorkerEvent) => void) =>
        handlers.set(type, handler),
    },
    caches,
    fetch,
    URL,
    Response,
  });
  return {
    cache,
    caches,
    fetch,
    async lifecycle(type: "install" | "activate") {
      const pending: Promise<unknown>[] = [];
      handlers.get(type)!({
        request: new Request("https://chat.example/"),
        waitUntil: (promise) => pending.push(promise),
        respondWith: () => {
          throw new Error("Lifecycle events cannot respond to requests");
        },
      });
      await Promise.all(pending);
    },
    request(request: Request) {
      let response: Promise<Response> | undefined;
      handlers.get("fetch")!({
        request,
        waitUntil: () => {},
        respondWith: (promise) => {
          response = promise;
        },
      });
      return response;
    },
  };
}

describe("shell-only PWA", () => {
  beforeAll(async () => {
    source = (await buildWorker(resolve("."), paths, "abcdef12")).code;
  });

  it("precaches only the injected shell and deletes only older q15 shell caches", async () => {
    const sw = worker();
    await sw.lifecycle("install");
    expect(sw.caches.open).toHaveBeenCalledWith(cacheName);
    expect(sw.cache.addAll).toHaveBeenCalledWith(paths);
    await sw.lifecycle("activate");
    expect(sw.caches.delete.mock.calls).toEqual([["q15-shell-old"]]);
  });

  it("does not intercept API, sockets, media, query strings, other origins or writes", () => {
    const sw = worker();
    const excluded = [
      new Request("https://chat.example/api/turns?after_seq=0"),
      new Request("https://chat.example/api/turns"),
      new Request("https://chat.example/ws"),
      new Request("https://chat.example/media/private.png"),
      new Request("https://chat.example/assets/app-abcdef12.js?token=private"),
      new Request("https://other.example/assets/app-abcdef12.js"),
      new Request("https://chat.example/index.html", { method: "POST" }),
      new Request("https://chat.example/assets/app-abcdef12.js", { method: "HEAD" }),
      new Request("https://chat.example/unlisted.html"),
    ];
    for (const request of excluded) expect(sw.request(request)).toBeUndefined();
    expect(sw.fetch).not.toHaveBeenCalled();
    expect(sw.caches.open).not.toHaveBeenCalled();
  });

  it("uses the network for HTML and the precached entry when navigation is offline", async () => {
    const sw = worker();
    const request = new Request("https://chat.example/");
    Object.defineProperty(request, "mode", { value: "navigate" });
    expect(await (await sw.request(request))!.text()).toBe("network");
    expect(sw.cache.match).not.toHaveBeenCalled();
    sw.fetch.mockRejectedValue(new Error("offline"));
    expect(await (await sw.request(request))!.text()).toBe("cached /index.html");
    expect(sw.cache.put).not.toHaveBeenCalled();
  });

  it("returns a network error when both navigation and the cached HTML are unavailable", async () => {
    const sw = worker();
    sw.fetch.mockRejectedValue(new Error("offline"));
    sw.cache.match.mockResolvedValue(undefined);
    expect((await sw.request(new Request("https://chat.example/index.html")))!.type).toBe("error");
  });

  it("serves cached assets and fetches cache misses without storing runtime responses", async () => {
    const sw = worker();
    const request = new Request("https://chat.example/assets/app-abcdef12.js");
    expect(await (await sw.request(request))!.text()).toBe("cached /assets/app-abcdef12.js");
    expect(sw.fetch).not.toHaveBeenCalled();
    sw.cache.match.mockResolvedValue(undefined);
    expect(await (await sw.request(request))!.text()).toBe("network");
    expect(sw.cache.put).not.toHaveBeenCalled();
    expect(sw.cache.addAll).not.toHaveBeenCalled();
  });
  it("publishes installable paths with standalone icons", () => {
    const manifest = JSON.parse(readFileSync("public/manifest.webmanifest", "utf8"));
    expect(manifest.display).toBe("standalone");
    expect(manifest.start_url).toBe("/");
    expect(manifest.icons.map((icon: { sizes: string }) => icon.sizes)).toEqual([
      "192x192",
      "512x512",
      "any",
    ]);
  });
});

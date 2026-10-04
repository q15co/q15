import { webcrypto } from "node:crypto";
// @vitest-environment node
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { runInNewContext } from "node:vm";
import { build } from "vite-plus";
import { afterEach, beforeAll, describe, expect, it, vi } from "vite-plus/test";

import { parseShellManifest } from "../src/shared/shell-manifest";
import { required } from "../src/testing/required";
import { sessionKey } from "../src/testing/session-key";
import { contentWorker } from "./content-worker";
import { buildWorker, shell } from "./shell";

const paths = ["/index.html", "/assets/app-abcdef12.js"];
const cacheName = "q15-shell-abcdef12";
let source: string;

interface WorkerEvent {
  request: Request;
  waitUntil: (promise: Promise<unknown>) => void;
  respondWith: (promise: Promise<Response>) => void;
}

const cacheMiss: Response | undefined = undefined;

async function worker(mode: "source" | "compiled") {
  const signer = await sessionKey();
  const handlers = new Map<string, (event: WorkerEvent) => void>();
  const cache = {
    addAll: vi.fn<(paths: string[]) => Promise<void>>().mockResolvedValue(),
    match: vi.fn<(path: string) => Promise<Response | undefined>>(
      (path: string): Promise<Response | undefined> =>
        Promise.resolve(new Response(`cached ${path}`)),
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
  const environment = {
    self: {
      location: { origin: "https://chat.example" },
      clients: { claim: vi.fn<() => Promise<void>>().mockResolvedValue() },
      addEventListener: (type: string, handler: (event: WorkerEvent) => void) =>
        handlers.set(type, handler),
    },
    caches,
    fetch,
    URL,
    Response,
    Request,
    Headers,
    indexedDB: signer.indexedDB,
    crypto: webcrypto,
    CryptoKey: signer.key.constructor,
    TextEncoder,
    btoa,
  };
  if (mode === "compiled") runInNewContext(source, environment);
  else {
    vi.resetModules();
    for (const [key, value] of Object.entries(environment)) vi.stubGlobal(key, value);
    vi.stubGlobal("__SHELL__", { paths, version: "abcdef12" });
    // Keep the worker's WebWorker type environment separate from the DOM test project.
    await vi.importActual("../worker/sw.ts");
  }
  return {
    clients: environment.self.clients,
    cache,
    caches,
    fetch,
    async lifecycle(type: "install" | "activate") {
      const pending: Promise<unknown>[] = [];
      required(handlers.get(type))({
        request: new Request("https://chat.example/"),
        waitUntil: (promise) => {
          pending.push(promise);
        },
        respondWith: () => {
          throw new Error("Lifecycle events cannot respond to requests");
        },
      });
      await Promise.all(pending);
    },
    request(request: Request) {
      let response: Promise<Response> | undefined;
      required(handlers.get("fetch"))({
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

afterEach(() => vi.unstubAllGlobals());

async function shellBundle(content: string, scriptEndTag = "</script>") {
  const result = await build({
    root: resolve("."),
    configFile: false,
    logLevel: "silent",
    plugins: [
      contentWorker(),
      shell(),
      {
        name: "test-html",
        transformIndexHtml: {
          order: "post",
          handler: (html) =>
            html.replace("</head>", `${content}</head>`).replace("</script>", scriptEndTag),
        },
      },
    ],
    build: { write: false },
  });
  if (Array.isArray(result) || !("output" in result)) throw new Error("Expected one app bundle");
  const manifest = required(
    result.output.find((entry) => entry.fileName === "shell-manifest.json"),
  );
  if (manifest.type !== "asset" || typeof manifest.source !== "string")
    throw new Error("Expected a shell manifest asset");
  const decoded: unknown = JSON.parse(manifest.source);
  return { manifest: parseShellManifest(decoded), output: result.output };
}

it("builds an exact shell manifest and changes its cache version when HTML changes", async () => {
  const original = await shellBundle("");
  expect(original.manifest.paths).toEqual(
    [
      "/icon-192.png",
      "/icon-512.png",
      "/icon.svg",
      "/index.html",
      "/manifest.webmanifest",
      ...original.output
        .filter((entry) => entry.fileName.startsWith("assets/"))
        .map((entry) => `/${entry.fileName}`),
    ].toSorted(),
  );
  expect(original.output.map((entry) => entry.fileName)).toContain("sw.js");
  const html = required(original.output.find((entry) => entry.fileName === "index.html"));
  if (html.type !== "asset" || typeof html.source !== "string")
    throw new Error("Expected shell HTML");
  expect(html.source).toContain(
    `href="data:image/svg+xml,${encodeURIComponent(readFileSync("public/icon.svg", "utf8"))}"`,
  );
  expect(html.source).not.toContain('rel="manifest"');
  const changed = await shellBundle('<meta name="test-content" content="changed">');
  expect(changed.manifest.paths).toEqual(original.manifest.paths);
  expect(changed.manifest.version).not.toBe(original.manifest.version);
});

it.each(["</script >", "</SCRIPT >", '</script foo="bar">'])(
  "inlines the module when HTML uses the browser-accepted end tag %s",
  async (endTag) => {
    const bundle = await shellBundle("", endTag);
    const html = required(bundle.output.find((entry) => entry.fileName === "index.html"));
    if (html.type !== "asset" || typeof html.source !== "string")
      throw new Error("Expected a shell HTML asset");
    expect(html.source).toContain('<script nonce="__Q15_NONCE__" type="module">');
    expect(html.source).not.toContain(endTag);
    expect(html.source).not.toContain('src="/assets/');
  },
);

describe.each(["source", "compiled"] satisfies ("source" | "compiled")[])(
  "%s shell-only PWA",
  (mode) => {
    beforeAll(async () => {
      source = (await buildWorker(resolve("."), paths, "abcdef12")).code;
    });

    it("validates decoded shell manifests before using their paths", () => {
      const manifest = { version: "abcdef12", paths };
      expect(parseShellManifest(manifest)).toBe(manifest);
      for (const value of [
        null,
        { version: 42, paths },
        { version: "abcdef12", paths: "/index.html" },
        { version: "abcdef12", paths: ["/index.html", null] },
      ])
        expect(() => parseShellManifest(value)).toThrow(/unsupported/iu);
    });

    it("precaches only the injected shell and deletes only older q15 shell caches", async () => {
      const sw = await worker(mode);
      await sw.lifecycle("install");
      expect(sw.caches.open).toHaveBeenCalledWith(cacheName);
      expect(
        sw.cache.put.mock.calls
          .map((call) =>
            typeof call[0] === "string"
              ? call[0]
              : new URL(call[0] instanceof Request ? call[0].url : call[0]).pathname,
          )
          .toSorted(),
      ).toEqual(paths.toSorted());
      expect(sw.fetch).toHaveBeenCalledTimes(paths.length);
      await sw.lifecycle("activate");
      expect(sw.caches.delete.mock.calls).toEqual([["q15-shell-old"]]);
      expect(sw.clients.claim).toHaveBeenCalledOnce();
    });

    it("refuses to cache unauthenticated installation responses", async () => {
      const sw = await worker(mode);
      sw.fetch.mockResolvedValue(new Response("unauthorized", { status: 401 }));
      await expect(sw.lifecycle("install")).rejects.toThrow("Shell authentication failed");
      expect(sw.cache.put).not.toHaveBeenCalled();
    });

    it("does not intercept API, sockets, media, query strings, other origins or writes", async () => {
      const sw = await worker(mode);
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
      const sw = await worker(mode);
      const request = new Request("https://chat.example/");
      Object.defineProperty(request, "mode", { value: "navigate" });
      expect(await required(await sw.request(request)).text()).toBe("network");
      expect(sw.cache.match).not.toHaveBeenCalled();
      sw.fetch.mockRejectedValue(new Error("offline"));
      expect(await required(await sw.request(request)).text()).toBe("cached /index.html");
      expect(sw.cache.put).not.toHaveBeenCalled();
    });

    it("returns a network error when both navigation and the cached HTML are unavailable", async () => {
      const sw = await worker(mode);
      sw.fetch.mockRejectedValue(new Error("offline"));
      sw.cache.match.mockResolvedValue(cacheMiss);
      expect(required(await sw.request(new Request("https://chat.example/index.html"))).type).toBe(
        "error",
      );
    });

    it("serves cached assets and fetches cache misses without storing runtime responses", async () => {
      const sw = await worker(mode);
      const request = new Request("https://chat.example/assets/app-abcdef12.js", {
        credentials: "omit",
        mode: "no-cors",
      });
      expect(await required(await sw.request(request)).text()).toBe(
        "cached /assets/app-abcdef12.js",
      );
      expect(sw.fetch).not.toHaveBeenCalled();
      sw.cache.match.mockResolvedValue(cacheMiss);
      expect(await required(await sw.request(request)).text()).toBe("network");
      expect(sw.fetch.mock.calls[0]?.[0].credentials).toBe("same-origin");
      expect(sw.fetch.mock.calls[0]?.[0].mode).toBe("same-origin");
      expect(sw.fetch.mock.calls[0]?.[0].headers.has("Q15-Proof")).toBe(true);
      expect(sw.cache.put).not.toHaveBeenCalled();
      expect(sw.cache.addAll).not.toHaveBeenCalled();
    });
    it("publishes installable paths with standalone icons", () => {
      const manifest: unknown = JSON.parse(readFileSync("public/manifest.webmanifest", "utf8"));
      expect(manifest).toMatchObject({
        display: "standalone",
        start_url: "/",
        icons: [{ sizes: "192x192" }, { sizes: "512x512" }, { sizes: "any" }],
      });
    });
  },
);

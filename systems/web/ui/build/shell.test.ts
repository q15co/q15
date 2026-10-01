import { readFileSync } from "node:fs";
import { describe, expect, it } from "vite-plus/test";
import { workerSource } from "./shell";

describe("shell-only PWA", () => {
  it("uses build-derived caches and explicitly bypasses messages, sockets, and non-shell URLs", () => {
    const source = workerSource(["/index.html", "/assets/app-abcdef12.js"], "abcdef12");
    expect(source).toContain('"q15-shell-abcdef12"');
    expect(source).toContain('request.method !== "GET"');
    expect(source).toContain("url.search || !SHELL.includes(path)");
    expect(source).not.toContain("cache.put");
    expect(source).not.toContain("/api");
    expect(source).not.toContain("/ws");
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

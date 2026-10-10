import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { ShareInbox } from "./share-inbox";

import {
  ShareCacheName,
  ShareFilenameHeader,
  shareEntryPath,
  ShareLifetimeMs,
} from "../shared/share-inbox";
import { dropExpiredShares, takeSharedFiles } from "./share-inbox";

const receivedAt = 1_700_000_000_000;
const now = receivedAt + 500;

interface Held {
  readonly path: string;
  readonly response: Response;
}

function share(files: readonly { name: string; type: string; body: string }[]): Held[] {
  return files.map((file, index) => ({
    path: shareEntryPath(receivedAt, index),
    response: new Response(file.body, {
      headers: { [ShareFilenameHeader]: encodeURIComponent(file.name), "content-type": file.type },
    }),
  }));
}

function held(entries: readonly Held[]) {
  const requests = entries.map(
    (entry) => new Request(new URL(entry.path, "https://chat.example").href),
  );
  const responses = new Map(
    requests.map((request, index) => [request.url, entries[index]?.response]),
  );
  const deleted: string[] = [];
  const inbox: ShareInbox = {
    keys: () => Promise.resolve(requests),
    match: (request) => Promise.resolve(responses.get(request.url)),
    delete: (request) => {
      deleted.push(new URL(request.url).pathname);
      return Promise.resolve(true);
    },
  };
  return { deleted, open: () => Promise.resolve(inbox) };
}

afterEach(() => vi.unstubAllGlobals());

describe("takeSharedFiles", () => {
  it("returns what an Android share left behind and empties the inbox", async () => {
    const entries = share([
      { name: "scan.pdf", type: "application/pdf", body: "scan" },
      { name: "übersicht.png", type: "image/png", body: "img" },
    ]);
    const { deleted, open } = held(entries);
    const taken = await takeSharedFiles(now, open);
    expect(taken.map((file) => [file.name, file.type, file.size])).toEqual([
      ["scan.pdf", "application/pdf", 4],
      ["übersicht.png", "image/png", 3],
    ]);
    expect(deleted).toEqual(entries.map((entry) => entry.path));
  });
  it("drops a stale entry and anything that is not a share entry", async () => {
    const entries = [
      ...share([{ name: "scan.pdf", type: "application/pdf", body: "scan" }]),
      { path: "/share-inbox/stale-0", response: new Response("older") },
      { path: shareEntryPath(now - 24 * 60 * 60 * 1000 - 1, 0), response: new Response("stale") },
    ];
    const { deleted, open } = held(entries);
    expect(await takeSharedFiles(now, open)).toHaveLength(1);
    expect(deleted).toEqual(entries.map((entry) => entry.path));
  });
  it("names an entry from its path when the filename header is missing or unusable", async () => {
    const entries = [
      { path: shareEntryPath(receivedAt, 0), response: new Response("a") },
      {
        path: shareEntryPath(receivedAt, 1),
        response: new Response("b", { headers: { [ShareFilenameHeader]: "%" } }),
      },
      {
        path: shareEntryPath(receivedAt, 2),
        response: new Response("c", { headers: { [ShareFilenameHeader]: "" } }),
      },
      {
        path: shareEntryPath(receivedAt, 3),
        response: new Response(null, { headers: { [ShareFilenameHeader]: "typeless.bin" } }),
      },
    ];
    const { open } = held(entries);
    const taken = await takeSharedFiles(now, open);
    expect(taken.map((file) => file.name)).toEqual([
      "1700000000000-0",
      "shared-file",
      "shared-file",
      "typeless.bin",
    ]);
    expect(taken[3]?.type).toBe("");
  });
  it("reads the browser cache the service worker wrote into", async () => {
    const { deleted, open } = held([
      ...share([{ name: "scan.pdf", type: "application/pdf", body: "scan" }]),
    ]);
    const cache = await open();
    const opened = vi.fn<() => Promise<ShareInbox | undefined>>(() => Promise.resolve(cache));
    vi.stubGlobal("caches", {
      keys: () => Promise.resolve([ShareCacheName]),
      open: opened,
    });
    expect((await takeSharedFiles(now)).map((file) => file.name)).toEqual(["scan.pdf"]);
    expect(opened).toHaveBeenCalledWith("q15-share");
    expect(deleted).toEqual([shareEntryPath(receivedAt, 0)]);
  });
  it("never creates an inbox the service worker has not written into", async () => {
    const opened = vi.fn<() => Promise<ShareInbox | undefined>>();
    vi.stubGlobal("caches", { keys: () => Promise.resolve(["q15-shell-abcdef12"]), open: opened });
    await expect(takeSharedFiles(now)).resolves.toEqual([]);
    await expect(dropExpiredShares(now)).resolves.toBeUndefined();
    expect(opened).not.toHaveBeenCalled();
  });
  it("returns nothing when the browser has no cache storage", async () => {
    vi.stubGlobal("caches", undefined);
    await expect(takeSharedFiles(now)).resolves.toEqual([]);
  });
  it("drops only the shares nobody claimed in time", async () => {
    const stale = shareEntryPath(now - ShareLifetimeMs - 1, 0);
    const { deleted, open } = held([
      ...share([{ name: "scan.pdf", type: "application/pdf", body: "scan" }]),
      { path: stale, response: new Response("stale") },
    ]);
    await expect(dropExpiredShares(now, open)).resolves.toBeUndefined();
    expect(deleted).toEqual([stale]);
  });
  it("sweeps nothing when the browser has no cache storage", async () => {
    vi.stubGlobal("caches", undefined);
    await expect(dropExpiredShares(now)).resolves.toBeUndefined();
  });
});

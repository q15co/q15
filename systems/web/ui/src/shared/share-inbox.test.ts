import { describe, expect, it } from "vite-plus/test";

import { MaxMediaFiles } from "../generated/protocol";
import {
  isExpiredShare,
  parseShareEntry,
  ShareLifetimeMs,
  ShareMaxBytes,
  ShareRejected,
  shareEntryPath,
  shareFits,
  shareNotice,
} from "./share-inbox";

describe("share inbox layout", () => {
  it("round-trips an entry path", () => {
    const path = shareEntryPath(1_700_000_000_000, 3);
    expect(path).toBe("/share-inbox/1700000000000-3");
    expect(parseShareEntry(path)).toEqual({ receivedAt: 1_700_000_000_000, index: 3, path });
  });
  it.each([
    "/share-inbox",
    "/other/1700000000000-0",
    "/share-inbox/1700000000000",
    "/share-inbox/1700000000000-0-1",
    "/share-inbox/stamp-0",
    "/share-inbox/1700000000000-index",
    "/share-inbox/0-0",
    "/share-inbox/9007199254740993-0",
  ])("ignores the entry path %s", (path) => {
    expect(parseShareEntry(path)).toBeUndefined();
  });
  it("expires an entry a day after it arrived, not before", () => {
    const entry = { receivedAt: 1_000, index: 0, path: shareEntryPath(1_000, 0) };
    expect(isExpiredShare(entry, 1_000 + ShareLifetimeMs)).toBe(false);
    expect(isExpiredShare(entry, 1_001 + ShareLifetimeMs)).toBe(true);
  });
  it("takes only what the composer could send, and only up to the storage backstop", () => {
    expect(shareFits([])).toBe(false);
    expect(shareFits([1])).toBe(true);
    expect(shareFits(Array.from({ length: MaxMediaFiles }, () => 1))).toBe(true);
    expect(shareFits(Array.from({ length: MaxMediaFiles + 1 }, () => 1))).toBe(false);
    expect(shareFits([ShareMaxBytes])).toBe(true);
    expect(shareFits([ShareMaxBytes, 1])).toBe(false);
  });
  it("explains only the rejection it recognises", () => {
    expect(shareNotice(ShareRejected)).toContain("could not attach");
    expect(shareNotice("other")).toBeUndefined();
    expect(shareNotice(null)).toBeUndefined();
  });
});

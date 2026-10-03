import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import history from "../fixtures/server/history.json";
import { fetchHistory } from "./history";

afterEach(() => vi.unstubAllGlobals());

describe("HTTP history adapter", () => {
  it("requests uncached same-origin history and forwards cancellation", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(() => Promise.resolve(Response.json(history)));
    vi.stubGlobal("fetch", fetch);
    const controller = new AbortController();
    expect(await fetchHistory("42", controller.signal)).toEqual(history);
    expect(fetch).toHaveBeenCalledWith("/api/turns?after_seq=42&limit=50", {
      credentials: "same-origin",
      cache: "no-store",
      signal: controller.signal,
    });
    await fetchHistory("0");
    expect(fetch).toHaveBeenLastCalledWith("/api/turns?after_seq=0&limit=50", {
      credentials: "same-origin",
      cache: "no-store",
    });
  });

  it.each([
    { status: 401, message: "Sign in again" },
    { status: 503, message: "History could not be loaded" },
  ])("surfaces HTTP $status without trusting its response body", async ({ status, message }) => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response("", { status })));
    await expect(fetchHistory("0")).rejects.toThrow(message);
  });

  it("rejects malformed successful responses and propagates cancellation", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json({ turns: [] }));
    vi.stubGlobal("fetch", fetch);
    await expect(fetchHistory("0")).rejects.toThrow("unsupported history");
    const abort = new DOMException("cancelled", "AbortError");
    fetch.mockRejectedValue(abort);
    await expect(fetchHistory("0", AbortSignal.abort())).rejects.toBe(abort);
  });
});

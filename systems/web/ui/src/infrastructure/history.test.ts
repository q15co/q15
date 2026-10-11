import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import history from "../fixtures/server/history.json";
import { required } from "../testing/required";
import { proofHeaders, sessionKey } from "../testing/session-key";
import { frame } from "./envelope";
import { fetchHistory } from "./history";
import { ContentSession } from "./seal";

const content = new ContentSession();
beforeEach(sessionKey);

afterEach(() => vi.unstubAllGlobals());

describe("HTTP history adapter", () => {
  it("requests uncached same-origin history and forwards cancellation", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(() =>
      Promise.resolve(Response.json(frame("history", history, "channel"))),
    );
    vi.stubGlobal("fetch", fetch);
    const controller = new AbortController();
    expect(await fetchHistory("42", controller.signal, content)).toEqual(history);
    expect(fetch).toHaveBeenCalledWith("/api/turns?after_seq=42&limit=10", {
      credentials: "same-origin",
      cache: "no-store",
      headers: proofHeaders,
      signal: controller.signal,
    });
    expect(new Headers(required(fetch.mock.calls[0])[1]?.headers).get("Q15-Channel")).toBe(
      "channel",
    );
    await fetchHistory("0", undefined, content);
    expect(fetch).toHaveBeenLastCalledWith("/api/turns?after_seq=0&limit=5", {
      credentials: "same-origin",
      cache: "no-store",
      headers: proofHeaders,
    });
  });

  it.each([
    { status: 401, message: "Sign in again" },
    { status: 413, message: "A history turn is too large to load" },
    { status: 503, message: "History could not be loaded" },
  ])("surfaces HTTP $status without trusting its response body", async ({ status, message }) => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response("", { status })));
    await expect(fetchHistory("0", undefined, content)).rejects.toThrow(message);
  });

  it("rejects malformed successful responses and propagates cancellation", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json({ turns: [] }));
    vi.stubGlobal("fetch", fetch);
    await expect(fetchHistory("0", undefined, content)).rejects.toThrow("unsupported chat frame");
    const abort = new DOMException("cancelled", "AbortError");
    fetch.mockRejectedValue(abort);
    await expect(fetchHistory("0", AbortSignal.abort(), content)).rejects.toBe(abort);
  });
  it("refuses history for another channel or a non-history payload", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>();
    vi.stubGlobal("fetch", fetch);
    for (const value of [
      frame("history", history, "wrong-channel"),
      frame("notice", {}, "channel"),
    ]) {
      fetch.mockResolvedValueOnce(Response.json(value));
      await expect(fetchHistory("0", undefined, content)).rejects.toThrow("unsupported history");
    }
  });
});

vi.mock("./seal", () => ({
  ContentSession: class {
    channel() {
      return Promise.resolve("channel");
    }
    open(value: unknown) {
      return Promise.resolve(value);
    }
  },
}));

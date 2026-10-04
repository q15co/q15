import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { ContentCommand, ContentResult } from "../domain/content-rpc";
import type { ContentWorkerPort } from "./content-worker";

import { MaxContentJobs, MaxQueuedContentBytes, parseContentRequest } from "../domain/content-rpc";
import history from "../fixtures/server/history.json";
import { required } from "../testing/required";
import { ContentWorker } from "./content-worker";
import { clientFrame } from "./envelope";

class Port implements ContentWorkerPort {
  onmessage: ContentWorkerPort["onmessage"] = null;
  onerror: ContentWorkerPort["onerror"] = null;
  onmessageerror: ContentWorkerPort["onmessageerror"] = null;
  postMessage = vi.fn<(value: unknown, transfer: Transferable[]) => void>();
  terminate = vi.fn<() => void>();
  request(index = -1) {
    return parseContentRequest(required(this.postMessage.mock.calls.at(index))[0]);
  }
  reply(result: ContentResult, index = -1, refresh = false) {
    const { id, generation } = this.request(index);
    this.onmessage?.(
      new MessageEvent("message", { data: { id, generation, result, refresh, elapsed: 2 } }),
    );
  }
}
function setup() {
  const ports: Port[] = [];
  const factory = vi.fn<() => Port>(() => {
    const port = new Port();
    ports.push(port);
    return port;
  });
  const content = new ContentWorker(factory);
  content.onFailure = vi.fn<ContentWorker["onFailure"]>();
  content.onRefresh = vi.fn<ContentWorker["onRefresh"]>();
  content.onTiming = vi.fn<ContentWorker["onTiming"]>();
  const offer = content.offer("0", "binding");
  const port = required(ports[0]);
  port.reply({ kind: "wire", data: "hello" });
  return { content, factory, ports, port, offer };
}
async function keyed() {
  const result = setup();
  await result.offer;
  const key = result.content.receive("key");
  result.port.reply({ kind: "key", channel: "channel" });
  await key;
  return result;
}
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
describe("dedicated content worker", () => {
  it("starts the bundled blob worker and revokes its URL", async () => {
    const port = new Port();
    const create = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:worker");
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    const factory = vi.fn<() => Port>(function () {
      return port;
    });
    vi.stubGlobal("Worker", factory);
    const content = new ContentWorker();
    const offer = content.offer("0", "binding");
    port.reply({ kind: "wire", data: "hello" });
    expect(await offer).toBe("hello");
    expect(create).toHaveBeenCalledWith(expect.any(Blob));
    expect(factory).toHaveBeenCalledWith("blob:worker", { name: "q15-content" });
    expect(revoke).toHaveBeenCalledWith("blob:worker");
    content.reset();
    expect(port.terminate).toHaveBeenCalledOnce();
  });
  it("shares a public channel lease, transfers history bytes and emits ordered results", async () => {
    const { content, port } = await keyed();
    const lease = await content.channel();
    expect(lease.id).toBe("channel");
    const bytes = new ArrayBuffer(12);
    const page = content.history(bytes, lease);
    expect(port.postMessage).toHaveBeenLastCalledWith(
      expect.objectContaining({ op: "history", data: bytes }),
      [bytes],
    );
    port.reply({ kind: "page", page: history }, -1, true);
    expect(await page).toEqual(history);
    const sending = content.send(clientFrame("presence", { fg: true }));
    port.reply({ kind: "wire", data: "encoded" }, -1, true);
    expect(await sending).toBe("encoded");
    expect(content.needsRefresh).toBe(true);
    expect(content.onRefresh).toHaveBeenCalledOnce();
    expect(content.onTiming).toHaveBeenCalledWith(
      expect.objectContaining({ op: "send", worker: 2 }),
    );
    content.reset();
    expect(content.needsRefresh).toBe(false);
    await expect(content.history(new ArrayBuffer(0), lease)).rejects.toMatchObject({
      name: "AbortError",
    });
  });
  it.each(["send", "history"] satisfies ContentCommand["op"][])(
    "propagates correlated %s errors",
    async (op) => {
      const { content, port } = await keyed();
      const pending =
        op === "send"
          ? content.send(clientFrame("ping", {}))
          : content.history(new ArrayBuffer(0), await content.channel());
      port.reply({ kind: "error", code: "content_too_large", message: "Too large" });
      await expect(pending).rejects.toMatchObject({
        code: "content_too_large",
        message: "Too large",
      });
      content.reset();
    },
  );
  it("aborts waiting channels, queued history and already aborted requests", async () => {
    const { content, port } = setup();
    const waiting = content.channel();
    const abort = new AbortController();
    const cancelled = content.channel(abort.signal);
    abort.abort();
    await expect(cancelled).rejects.toMatchObject({ name: "AbortError" });
    await expect(content.channel(abort.signal)).rejects.toMatchObject({ name: "AbortError" });
    content.reset();
    await expect(waiting).rejects.toMatchObject({ name: "AbortError" });
    const live = await keyed();
    const channel = await live.content.channel();
    await expect(
      live.content.history(new ArrayBuffer(0), channel, AbortSignal.abort()),
    ).rejects.toMatchObject({ name: "AbortError" });
    const controller = new AbortController();
    const page = live.content.history(new ArrayBuffer(0), channel, controller.signal);
    const request = live.port.request();
    controller.abort();
    await expect(page).rejects.toMatchObject({ name: "AbortError" });
    expect(live.port.postMessage).toHaveBeenLastCalledWith(
      { op: "cancel", id: request.id, generation: request.generation },
      [],
    );
    live.port.onmessage?.(
      new MessageEvent("message", {
        data: { ...request, refresh: false, elapsed: 0, result: { kind: "page", page: history } },
      }),
    );
    expect(live.content.onFailure).not.toHaveBeenCalled();
    live.content.reset();
    expect(port.terminate).toHaveBeenCalledOnce();
  });
  it("discards replies from retired workers and generations", async () => {
    const { content, port } = setup();
    const stale = required(port.onmessage);
    const request = port.request();
    content.reset();
    stale(new MessageEvent("message", { data: null }));
    const offering = content.offer("1", "binding");
    stale(new MessageEvent("message", { data: null }));
    // A reply carrying an old generation cannot satisfy a new job.
    const nextPort = new Port();
    const isolated = new ContentWorker(() => nextPort);
    const pending = isolated.offer("0", "binding");
    nextPort.onmessage?.(
      new MessageEvent("message", {
        data: {
          ...request,
          generation: 99,
          refresh: false,
          elapsed: 0,
          result: { kind: "wire", data: "stale" },
        },
      }),
    );
    nextPort.reply({ kind: "wire", data: "fresh" });
    expect(await pending).toBe("fresh");
    content.reset();
    await expect(offering).rejects.toMatchObject({ name: "AbortError" });
    expect(content.onFailure).not.toHaveBeenCalled();
    isolated.reset();
  });
  it.each(["onerror", "onmessageerror"] as const)(
    "settles all pending calls after %s and starts fresh",
    async (event) => {
      const { content, port, ports } = setup();
      const waiting = content.channel();
      const receiving = content.receive("incoming");
      if (event === "onerror") port.onerror?.(new ErrorEvent("error"));
      else port.onmessageerror?.(new MessageEvent("messageerror"));
      await expect(receiving).rejects.toThrow("stopped");
      await expect(waiting).rejects.toThrow("stopped");
      expect(vi.mocked(content.onFailure).mock.calls[0]?.[0].message).toContain("stopped");
      const offering = content.offer("1", "binding");
      required(ports[1]).reply({ kind: "wire", data: "fresh" });
      expect(await offering).toBe("fresh");
      content.reset();
    },
  );
  it.each([
    null,
    {
      id: 999,
      generation: 0,
      refresh: false,
      elapsed: 0,
      result: { kind: "wire", data: "unexpected" },
    },
    { id: 1, generation: 0, refresh: false, elapsed: 0, result: { kind: "page", page: history } },
  ])("rejects malformed or mismatched RPC without stranding a promise %#", async (data) => {
    const { content, port } = setup();
    const receiving = content.receive("incoming");
    port.onmessage?.(new MessageEvent("message", { data }));
    await expect(receiving).rejects.toThrow("processing failed");
    expect(content.onFailure).toHaveBeenCalledOnce();
    expect(port.terminate).toHaveBeenCalledOnce();
  });
  it("bounds queued job count and bytes and makes overflow recoverable", async () => {
    for (const mode of ["count", "bytes"]) {
      const { content, port } = setup();
      const jobs: Promise<unknown>[] = [];
      const count = mode === "count" ? MaxContentJobs + 1 : 1;
      for (let index = 0; index < count; index++)
        jobs.push(
          content.receive(
            mode === "bytes" ? "x".repeat(Math.ceil(MaxQueuedContentBytes / 3) + 1) : "frame",
          ),
        );
      const settled = await Promise.allSettled(jobs);
      expect(settled.every((result) => result.status === "rejected")).toBe(true);
      expect(vi.mocked(content.onFailure).mock.calls[0]?.[0].message).toContain("overloaded");
      expect(port.postMessage.mock.calls.length).toBeLessThanOrEqual(MaxContentJobs + 1);
      expect(port.terminate).toHaveBeenCalledOnce();
    }
  });
  it("handles startup, posting and timeout failures", async () => {
    const content = new ContentWorker(() => {
      throw new Error("blocked by CSP");
    });
    content.onFailure = vi.fn<ContentWorker["onFailure"]>();
    await expect(content.offer("0", "binding")).rejects.toThrow("could not start");
    expect(content.onFailure).toHaveBeenCalledOnce();
    await expect(content.receive("not connected")).rejects.toThrow("could not start");
    const live = setup();
    live.port.postMessage.mockImplementation(() => {
      throw new Error("clone failure");
    });
    await expect(live.content.receive("incoming")).rejects.toThrow("could not receive");
    expect(live.content.onFailure).toHaveBeenCalledOnce();
    vi.useFakeTimers();
    const stalled = setup();
    const pending = stalled.content.receive("incoming");
    vi.advanceTimersByTime(15_000);
    await expect(pending).rejects.toThrow("timed out");
    expect(vi.mocked(stalled.content.onFailure).mock.calls[0]?.[0].message).toContain("timed out");
  });
  it("handles a worker lost while history cancellation is posted", async () => {
    const { content, port } = await keyed();
    const controller = new AbortController();
    const pending = content.history(new ArrayBuffer(0), await content.channel(), controller.signal);
    port.postMessage.mockImplementation(() => {
      throw new Error("worker lost");
    });
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(content.onFailure).toHaveBeenCalledOnce();
  });
});

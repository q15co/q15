import { describe, expect, it, vi } from "vite-plus/test";

import type { ContentRequest, ContentResult } from "../domain/content-rpc";

import { MaxContentJobs, MaxQueuedContentBytes, parseContentReply } from "../domain/content-rpc";
import { required } from "../testing/required";
import { contentProcessor } from "./content-processor";

const request = (id: number) => ({ op: "receive", data: "frame", id, generation: 1 });
function setup() {
  let finish: ((value: ContentResult) => void) | undefined;
  const execute = vi.fn<(request: ContentRequest) => Promise<ContentResult>>(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  const post = vi.fn<(value: unknown) => void>();
  const receive = contentProcessor(post, { execute, needsRefresh: false });
  return {
    post,
    receive,
    execute,
    finish: () => required(finish)({ kind: "wire", data: "encoded" }),
  };
}
describe("serial content jobs", () => {
  it("processes a shared socket/history ledger in submission order", async () => {
    const { receive, execute, finish, post } = setup();
    receive(request(1));
    receive({ op: "history", id: 2, generation: 1, data: new ArrayBuffer(0), channel: "channel" });
    receive(request(3));
    expect(execute).toHaveBeenCalledOnce();
    finish();
    await vi.waitFor(() => expect(execute).toHaveBeenCalledTimes(2));
    finish();
    await vi.waitFor(() => expect(execute).toHaveBeenCalledTimes(3));
    finish();
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(3));
    expect(post.mock.calls.map((call) => parseContentReply(call[0]).id)).toEqual([1, 2, 3]);
  });
  it("cancels queued work and suppresses active results without releasing the active budget early", async () => {
    const { receive, execute, finish, post } = setup();
    receive({ op: "cancel", id: 0, generation: 1 });
    receive(request(1));
    receive(request(2));
    receive({ op: "cancel", id: 1, generation: 0 });
    receive({ op: "cancel", id: 1, generation: 1 });
    receive({ op: "cancel", id: 2, generation: 1 });
    receive(request(3));
    finish();
    await vi.waitFor(() => expect(execute).toHaveBeenCalledTimes(2));
    expect(execute.mock.calls.map((call) => call[0].id)).toEqual([1, 3]);
    expect(post).not.toHaveBeenCalled();
    finish();
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
  });
  it.each([new Error("bad frame"), "invalid rejection"])(
    "reports failed jobs and continues with the next frame %#",
    async (error) => {
      const post = vi.fn<(value: unknown) => void>();
      const execute = vi
        .fn<(request: ContentRequest) => Promise<ContentResult>>()
        .mockRejectedValueOnce(error)
        .mockResolvedValue({ kind: "wire", data: "valid" });
      const receive = contentProcessor(post, { execute, needsRefresh: true });
      receive(request(1));
      receive(request(2));
      await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(2));
      expect(parseContentReply(required(post.mock.calls[0])[0])).toMatchObject({
        refresh: true,
        result: { kind: "error", code: "invalid_frame" },
      });
      expect(parseContentReply(required(post.mock.calls[1])[0]).result).toEqual({
        kind: "wire",
        data: "valid",
      });
    },
  );
  it.each(["count", "bytes", "malformed"])(
    "fails closed on %s overflow or bad RPC",
    async (mode) => {
      const { receive, execute, finish, post } = setup();
      receive(request(1));
      if (mode === "count") for (let id = 2; id <= MaxContentJobs + 1; id++) receive(request(id));
      else if (mode === "bytes")
        receive({ ...request(2), data: "x".repeat(Math.ceil(MaxQueuedContentBytes / 3) + 1) });
      else receive(null);
      receive(request(999));
      finish();
      await Promise.resolve();
      expect(execute).toHaveBeenCalledOnce();
      expect(post).toHaveBeenCalledExactlyOnceWith({
        fatal: "Content worker stopped. Reconnect to try again.",
      });
    },
  );
  it("starts the real engine when no test engine is supplied", async () => {
    const post = vi.fn<(value: unknown) => void>();
    contentProcessor(post)(request(1));
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
    expect(parseContentReply(required(post.mock.calls[0])[0]).result).toMatchObject({
      kind: "error",
      code: "invalid_frame",
    });
  });
});

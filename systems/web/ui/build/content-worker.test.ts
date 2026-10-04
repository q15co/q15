// @vitest-environment node
import { webcrypto } from "node:crypto";
import { runInNewContext } from "node:vm";
import * as vite from "vite-plus";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { TestSealer } from "../e2e/seal";
import { parseContentReply } from "../src/domain/content-rpc";
import { parseClientFrame } from "../src/domain/protocol";
import history from "../src/fixtures/server/history.json";
import { frame } from "../src/infrastructure/envelope";
import { required } from "../src/testing/required";
import { compileContentWorker, contentWorker } from "./content-worker";

const containsKey = (value: unknown): boolean =>
  value instanceof CryptoKey ||
  (typeof value === "object" &&
    value !== null &&
    Object.values(value).some((item) => containsKey(item)));

vi.mock("vite-plus", { spy: true });

afterEach(() => vi.unstubAllGlobals());
describe.each(["source", "compiled"])("%s content worker", (mode) => {
  it("keeps keys local and authenticates history and concurrent socket replays through one ledger", async () => {
    let receive: ((event: MessageEvent<unknown>) => void) | undefined;
    const post = vi.fn<(value: unknown) => void>();
    const environment = {
      self: {
        postMessage: post,
        addEventListener: (_event: string, handler: (event: MessageEvent<unknown>) => void) => {
          receive = handler;
        },
      },
      crypto: webcrypto,
      TextEncoder,
      TextDecoder,
      Uint8Array,
      ArrayBuffer,
      DataView,
      Date,
      performance,
      atob,
      btoa,
    };
    if (mode === "source") {
      vi.resetModules();
      for (const [key, value] of Object.entries(environment)) vi.stubGlobal(key, value);
      await import("../worker/content");
    } else runInNewContext((await compileContentWorker(process.cwd())).code, environment);
    let id = 0;
    const submit = (command: object) =>
      required(receive)(
        new MessageEvent("message", { data: { ...command, id: id++, generation: 1 } }),
      );
    submit({ op: "offer", cursor: "0", binding: "binding" });
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const offer = parseContentReply(required(post.mock.calls[0])[0]);
    if (offer.result.kind !== "wire") throw new Error("Expected encoded hello");
    const hello = parseClientFrame(offer.result.data);
    if (hello.type !== "hello") throw new Error("Expected hello");
    const peer = new TestSealer(hello.payload);
    submit({ op: "receive", data: JSON.stringify(peer.key) });
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    const wire = JSON.stringify(peer.seal(frame("history", history, peer.channelID)));
    submit({ op: "history", data: new TextEncoder().encode(wire).buffer, channel: peer.channelID });
    submit({ op: "receive", data: wire });
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(4));
    expect(parseContentReply(required(post.mock.calls[2])[0]).result).toEqual({
      kind: "page",
      page: history,
    });
    expect(parseContentReply(required(post.mock.calls[3])[0]).result).toMatchObject({
      kind: "error",
      code: "unseal_failed",
    });
    const notice = JSON.stringify(
      peer.seal(frame("notice", { code: "info", text: "authenticated" })),
    );
    submit({ op: "receive", data: notice });
    submit({ op: "receive", data: notice });
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(6));
    expect(parseContentReply(required(post.mock.calls[4])[0]).result).toMatchObject({
      kind: "frame",
      frame: { payload: { text: "authenticated" } },
    });
    expect(parseContentReply(required(post.mock.calls[5])[0]).result).toMatchObject({
      kind: "error",
      code: "unseal_failed",
    });
    // The RPC contains domain results and public metadata, never CryptoKey handles.

    expect(post.mock.calls.some((call) => containsKey(call[0]))).toBe(false);
  });
});

it("invalidates the virtual worker bundle when its source changes", async () => {
  const plugin = contentWorker();
  if (typeof plugin.watchChange !== "function") throw new Error("Missing worker watch hook");
  const compile = vi.mocked(vite.build).mockResolvedValueOnce([]);
  await expect(compileContentWorker(process.cwd())).rejects.toThrow("self-contained");
  expect(compile).toHaveBeenCalledOnce();
  Reflect.apply(plugin.watchChange, {}, ["worker/content.ts", { event: "update" }]);
});

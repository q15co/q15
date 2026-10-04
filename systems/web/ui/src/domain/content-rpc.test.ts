import { describe, expect, it } from "vite-plus/test";

import frames from "../fixtures/protocol/frames.json";
import history from "../fixtures/server/history.json";
import { required } from "../testing/required";
const ready = required(frames.find((frame) => frame.type === "ready"));
const hello = required(frames.find((frame) => frame.type === "hello"));
const send = required(frames.find((frame) => frame.type === "msg.send"));
import {
  contentBytes,
  contentCancellation,
  parseContentReply,
  parseContentRequest,
} from "./content-rpc";

const metadata = { id: 3, generation: 1 };
const reply = { ...metadata, elapsed: 0, refresh: false };
describe("content worker boundary", () => {
  it("validates each command and conservatively accounts bytes without encoding", () => {
    const commands = [
      { ...metadata, op: "offer", cursor: "42", binding: "binding" },
      { ...metadata, op: "receive", data: "hello" },
      { ...metadata, op: "history", data: new ArrayBuffer(8), channel: "channel" },
      { ...metadata, op: "send", frame: send },
      { ...metadata, op: "send", frame: hello },
    ];
    for (const command of commands)
      expect(contentBytes(parseContentRequest(command))).toBeGreaterThan(0);
    expect(contentBytes(parseContentRequest(commands[1]))).toBe(15);
    expect(contentBytes(parseContentRequest(commands[2]))).toBe(8);
    expect(contentCancellation({ ...metadata, op: "cancel" })).toEqual(metadata);
    expect(() => {
      Reflect.apply(contentBytes, undefined, [{ op: "unknown" }]);
    }).toThrow("Unknown content operation");
  });
  it.each([
    null,
    {},
    { ...metadata, id: -1 },
    { ...metadata, generation: 0.5 },
    { ...metadata, op: "offer", cursor: "no", binding: "binding" },
    { ...metadata, op: "offer", cursor: "0", binding: null },
    { ...metadata, op: "offer", cursor: "0", binding: "a".repeat(129) },
    { ...metadata, op: "receive", data: null },
    { ...metadata, op: "history", data: "not bytes", channel: "channel" },
    { ...metadata, op: "history", data: new ArrayBuffer(0), channel: null },
    { ...metadata, op: "send", frame: {} },
    { ...metadata, op: "other" },
  ])("rejects an invalid command %#", (value) => {
    expect(() => parseContentRequest(value)).toThrow(/.+/u);
    expect(contentCancellation(value)).toBeNull();
  });
  it("returns only validated domain values and public metadata", () => {
    for (const result of [
      { kind: "wire", data: "encoded" },
      { kind: "key", channel: "channel" },
      { kind: "frame", frame: ready },
      { kind: "page", page: history },
      ...["unseal_failed", "message_too_large", "content_too_large", "invalid_frame"].map(
        (code) => ({ kind: "error", code, message: "failed" }),
      ),
    ])
      expect(parseContentReply({ ...reply, result })).toEqual({ ...reply, result });
  });
  it.each([
    null,
    {},
    { ...reply, id: Infinity },
    { ...reply, generation: -1 },
    { ...reply, refresh: null },
    { ...reply, elapsed: null },
    { ...reply, elapsed: NaN },
    { ...reply, elapsed: -1 },
    ...[
      null,
      {},
      { kind: "wire", data: null },
      { kind: "key", channel: null },
      { kind: "key", channel: "" },
      { kind: "key", channel: "x".repeat(129) },
      { kind: "frame", frame: {} },
      { kind: "page", page: {} },
      { kind: "error", code: "other", message: "failed" },
      { kind: "error", code: "unseal_failed", message: null },
    ].map((result) => ({ ...reply, result })),
  ])("rejects an invalid reply %#", (value) =>
    expect(() => parseContentReply(value)).toThrow(/.+/u),
  );
});

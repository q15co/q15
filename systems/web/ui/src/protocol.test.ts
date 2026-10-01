import { describe, expect, it } from "vite-plus/test";
import frames from "./fixtures/protocol/frames.json";
import parts from "./fixtures/protocol/parts.json";
import streamed from "./fixtures/server/streamed.json";
import aborted from "./fixtures/server/aborted.json";
import failed from "./fixtures/server/failed.json";
import resumed from "./fixtures/server/resumed.json";
import { compareSeq, frame, parseFrame, parsePage } from "./protocol";

describe("frozen browser contract", () => {
  it("decodes every server frame without losing content", () => {
    const server = frames.filter((f) =>
      [
        "ready",
        "turn.start",
        "delta",
        "snapshot",
        "msg.final",
        "msg.status",
        "notice",
        "pong",
        "error",
      ].includes(f.type),
    );
    for (const value of [
      ...server.filter((f) => f.type !== "msg.status" || "state" in f.payload),
      ...streamed,
      aborted,
      failed,
      ...resumed,
    ])
      expect(parseFrame(JSON.stringify(value))).toEqual(value);
  });
  it("preserves all canonical part types and int64 identities", () => {
    const page = parsePage(parts);
    expect(page.turns[0]?.messages[0]?.parts.map((p) => p.part_type)).toEqual([
      "reasoning",
      "tool_call",
      "tool_result",
      "media",
      "text",
    ]);
    expect(compareSeq(page.head_seq, page.turns[0]!.seq)).toBe(1);
    expect(compareSeq("9007199254740992", "9007199254740993")).toBe(-1);
  });
  it("pins outgoing envelopes and rejects incompatible data", () => {
    expect(frame("hello", { cursor: "0" }).v).toBe(1);
    expect(() => parseFrame(JSON.stringify({ ...streamed[0], v: 2 }))).toThrow(/unsupported/i);
    expect(() => parseFrame(JSON.stringify({ ...streamed[4], seq: 9007199254740992 }))).toThrow(
      /unsupported/i,
    );
    expect(() => parseFrame(JSON.stringify({ ...streamed[4], payload: { kind: "text" } }))).toThrow(
      /unsupported/i,
    );
    expect(() => parsePage({ ...parts, turns: [{ seq: 42 }] })).toThrow(/unsupported/i);
  });
});

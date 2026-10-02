import { describe, expect, expectTypeOf, it } from "vite-plus/test";

import type { SendRequest } from "../generated/protocol";

import frames from "../fixtures/protocol/frames.json";
import parts from "../fixtures/protocol/parts.json";
import aborted from "../fixtures/server/aborted.json";
import failed from "../fixtures/server/failed.json";
import resumed from "../fixtures/server/resumed.json";
import streamed from "../fixtures/server/streamed.json";
import { clientFrame, frame } from "../infrastructure/envelope";
import { required } from "../testing/required";
import { compareSeq, parseClientFrame, parseFrame, parsePage } from "./protocol";

describe("frozen browser contract", () => {
  it("validates client fixtures and preserves typed outgoing requests", () => {
    const client = frames.filter(
      (value) =>
        ["hello", "sync", "msg.send", "msg.abort", "msg.ack", "presence", "ping"].includes(
          value.type,
        ) ||
        (value.type === "msg.status" && !("state" in value.payload)),
    );
    expect(client).toHaveLength(8);
    for (const value of client) expect(parseClientFrame(JSON.stringify(value))).toEqual(value);
    const request = clientFrame("msg.send", { client_msg_id: "client-1", text: "hello" });
    expectTypeOf(request.payload).toEqualTypeOf<SendRequest>();
    expect(parseClientFrame(JSON.stringify(request))).toEqual(request);
    for (const [type, payload] of [
      ["hello", { cursor: 41 }],
      ["sync", { cursor: "-1" }],
      ["msg.send", { client_msg_id: "client-1", text: null }],
      ["msg.abort", { turn: 42 }],
      ["msg.ack", { seq: "9223372036854775808" }],
      ["presence", { fg: "true" }],
      ["msg.status", { unexpected: true }],
      ["ping", { unexpected: true }],
      ["ready", { cursor: "0", head_seq: "0", device_id: "device-1" }],
    ] satisfies [string, unknown][]) {
      expect(() => parseClientFrame(JSON.stringify(frame(type, payload)))).toThrow(/unsupported/iu);
    }
    expect(() => parseClientFrame(JSON.stringify({ ...request, v: 2 }))).toThrow(/unsupported/iu);
  });
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
    expect(compareSeq(page.head_seq, required(page.turns[0]).seq)).toBe(1);
    expect(compareSeq("9007199254740992", "9007199254740993")).toBe(-1);
  });
  it("pins outgoing envelopes and rejects incompatible data", () => {
    expect(frame("hello", { cursor: "0" }).v).toBe(1);
    expect(() => parseFrame(JSON.stringify({ ...streamed[0], v: 2 }))).toThrow(/unsupported/iu);
    expect(() => parseFrame(JSON.stringify({ ...streamed[4], seq: 9007199254740992 }))).toThrow(
      /unsupported/iu,
    );
    expect(() => parseFrame(JSON.stringify({ ...streamed[4], payload: { kind: "text" } }))).toThrow(
      /unsupported/iu,
    );
    expect(() => parsePage({ ...parts, turns: [{ seq: 42 }] })).toThrow(/unsupported/iu);
  });
  it("validates optional model loop counters in progress events", () => {
    const progress = {
      msg: { turn: "42", ordinal: -1 },
      seq: "3",
      kind: "model_start",
      text: "",
    };
    for (const type of ["delta", "snapshot"]) {
      for (const loop_turn of [undefined, 0, 1]) {
        const payload = loop_turn === undefined ? progress : { ...progress, loop_turn };
        const value = frame(type, payload);
        expect(parseFrame(JSON.stringify(value))).toEqual(value);
      }
      for (const loop_turn of [null, "1", 1.5]) {
        expect(() => parseFrame(JSON.stringify(frame(type, { ...progress, loop_turn })))).toThrow(
          /unsupported/iu,
        );
      }
    }
  });
  it("rejects nonempty pong payloads and unknown server events", () => {
    expect(() => parseFrame(JSON.stringify(frame("pong", { unexpected: true })))).toThrow(
      /unsupported/iu,
    );
    expect(() => parseFrame(JSON.stringify(frame("future.event", {})))).toThrow(/unsupported/iu);
  });
  it("rejects malformed nested parts in history and final events", () => {
    const turn = required(parts.turns[0]);
    const original = required(turn.messages[0]);
    for (const invalid of [
      null,
      { ordinal: "0", part_type: "text", text: "answer" },
      { ordinal: 0, part_type: "media", media_kind: 42 },
      { ordinal: 0, part_type: "tool_result", is_error: "true" },
      {
        ordinal: 0,
        part_type: "tool_call",
        tool_call: { id: "call-1", name: "bash", arguments: { command: "pwd" } },
      },
    ]) {
      const message = { ...original, parts: [invalid] };
      expect(() => parsePage({ ...parts, turns: [{ ...turn, messages: [message] }] })).toThrow(
        /unsupported/iu,
      );
      expect(() =>
        parseFrame(
          JSON.stringify(
            frame("msg.final", {
              msg: { turn: turn.seq, ordinal: original.ordinal },
              full_text: "answer",
              status: "completed",
              message,
            }),
          ),
        ),
      ).toThrow(/unsupported/iu);
    }
  });
  it("preserves unfamiliar part types for the renderer fallback", () => {
    const turn = required(parts.turns[0]);
    const value = {
      ...parts,
      turns: [
        {
          ...turn,
          messages: [
            { ...required(turn.messages[0]), parts: [{ ordinal: 0, part_type: "future_part" }] },
          ],
        },
      ],
    };
    expect(parsePage(value)).toEqual(value);
  });
});

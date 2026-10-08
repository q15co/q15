import { describe, expect, it } from "vite-plus/test";

import type { ChatMessage, ChatState } from "./chat";

import media from "../fixtures/protocol/media.json";
import history from "../fixtures/server/history.json";
import streamed from "../fixtures/server/streamed.json";
import { isRecord } from "../shared/type-guards";
import { presentTurn } from "./activity";
import { groupTurns, reconcileHistory, reduceFrame } from "./chat";
import { parseFrame, parsePage } from "./protocol";

function freeze<T>(value: T): T {
  if (Array.isArray(value)) for (const child of value) freeze(child);
  else if (isRecord(value)) for (const child of Object.values(value)) freeze(child);
  return Object.freeze(value);
}
function initial(): ChatState {
  return {
    connection: "connected",
    messages: [],
    live: null,
    pending: [],
    active: null,
    cursor: "0",
    hasMore: false,
    loadingHistory: false,
    notice: null,
    error: null,
  };
}

function packet(type: string, payload: unknown) {
  return parseFrame(
    JSON.stringify({ v: 3, id: "event", ts: "2026-10-01T00:00:00Z", seq: "0", type, payload }),
  );
}

describe("pure chat domain", () => {
  it.each([100, 1000])(
    "never reads the historical collection during live updates with %s rows",
    (size) => {
      const messages = new Proxy(
        Array.from({ length: size }, (): ChatMessage => ({
          key: "1:0",
          turn: "1",
          ts: "",
          ordinal: 0,
          role: "assistant",
          parts: [],
        })),
        {
          get: () => {
            throw new Error("A live frame accessed completed history");
          },
        },
      );
      let state: ChatState = { ...initial(), messages };
      const msg = { turn: "42", ordinal: -1 };
      const call = { id: "call", name: "exec", arguments: "{}" };
      for (const type of ["delta", "snapshot"]) {
        for (const kind of ["model_start", "text", "reasoning", "tool_call", "tool_result"]) {
          state = reduceFrame(state, packet(type, { msg, seq: "0", kind, text: "content", call }));
          expect(state.messages).toBe(messages);
        }
      }
      expect(state.live?.turn).toBe("42");
    },
  );
  it("replays deterministically without modifying frozen state or wire frames", () => {
    let state = freeze(initial());
    for (const fixture of streamed) {
      const frame = freeze(parseFrame(JSON.stringify(fixture)));
      const before = structuredClone(state);
      const next = reduceFrame(state, frame);
      expect(reduceFrame(state, frame)).toEqual(next);
      expect(state).toEqual(before);
      state = freeze(next);
    }
    expect(state.active).toBeNull();
    expect(state.messages.at(-1)?.parts.at(-1)?.text).toBe("answer");
  });

  it("pairs canonical activity without changing messages or part identities", () => {
    const state = freeze(reconcileHistory([], [], parsePage(history)));
    const before = structuredClone(state);
    const first = presentTurn(state.messages);
    expect(presentTurn(state.messages)).toEqual(first);
    expect(state).toEqual(before);
    expect(first.answers).toHaveLength(1);
    expect(reconcileHistory(state.messages, [], parsePage(history)).messages).toBe(state.messages);
  });

  it("correlates repeated sends in FIFO order and keeps failed and stale sends", () => {
    const pending = freeze([
      { id: "first", text: "hello", afterTurn: "0", state: "accepted" },
      { id: "second", text: "hello", afterTurn: "0", state: "queued" },
      { id: "failed", text: "hello", afterTurn: "0", state: "failed" },
      { id: "stale", text: "hello", afterTurn: "42", state: "uncertain" },
    ] satisfies ChatState["pending"]);
    const page = freeze(parsePage(history));
    const next = reconcileHistory([], pending, page);
    expect(next.pending.map((item) => item.id)).toEqual(["second", "failed", "stale"]);
    expect(pending).toHaveLength(4);
  });

  it("keeps an active empty turn stable and does not manufacture idle turns", () => {
    expect([...groupTurns([], "42").keys()]).toEqual(["42"]);
    expect(groupTurns([], null).size).toBe(0);
  });

  it("handles running and idle status without rewriting assigned pending sends", () => {
    const state = freeze({
      ...initial(),
      pending: [{ id: "sent", text: "hello", afterTurn: "0", state: "running", turn: "42" }],
    } satisfies ChatState);
    const running = reduceFrame(
      state,
      packet("msg.status", { turn: "42", state: "running", queued: false, client_msg_id: "sent" }),
    );
    expect(running.active).toBe("42");
    expect(running.pending).toEqual(state.pending);
    const idle = reduceFrame(
      running,
      packet("msg.status", { turn: "0", state: "idle", queued: false }),
    );
    expect(idle.active).toBeNull();
    expect(idle.pending).toEqual(state.pending);
  });

  it.each([false, true])(
    "retains attachments through acceptance, queueing and streaming (queued: %s)",
    (queued) => {
      const parts = freeze(media.parts);
      let state: ChatState = freeze({
        ...initial(),
        pending: [{ id: "with-files", text: "", parts, afterTurn: "0", state: "sending" }],
      } satisfies ChatState);
      const accepted = packet("msg.status", {
        turn: "42",
        state: queued ? "queued" : "accepted",
        queued,
        client_msg_id: "with-files",
      });
      const next = reduceFrame(state, accepted);
      expect(next.pending[0]?.parts).toBe(parts);
      expect(next.pending[0]?.state).toBe(queued ? "queued" : "accepted");
      state = freeze(next);
      for (const value of [
        packet("turn.start", { turn: "42", msg: { turn: "42", ordinal: -1 } }),
        packet("msg.status", {
          turn: "42",
          state: "accepted",
          queued: false,
          client_msg_id: "with-files",
        }),
        packet("delta", {
          msg: { turn: "42", ordinal: -1 },
          seq: "1",
          kind: "text",
          text: "Reading the attachment",
        }),
        packet("snapshot", {
          msg: { turn: "42", ordinal: -1 },
          seq: "2",
          kind: "text",
          text: "Still reading",
        }),
        packet("msg.final", {
          msg: { turn: "42", ordinal: -1 },
          status: "completed",
          full_text: "Done",
        }),
      ]) {
        state = freeze(reduceFrame(state, value));
        expect(state.pending[0]?.parts).toBe(parts);
      }
    },
  );

  it.each([
    { code: "bridge_unavailable", text: "agent is unavailable" },
    { code: "too_many_devices", text: "Too many chat windows" },
    { code: "invalid_message", text: "message could not be accepted" },
    { code: "protocol_mismatch", text: "Refresh this page" },
    { code: "future_error", text: "Chat error: future_error" },
  ])("surfaces $code and fails only the correlated send", ({ code, text }) => {
    const state = freeze({
      ...initial(),
      pending: [
        { id: "failed", text: "hello", afterTurn: "0", state: "sending" },
        { id: "keep", text: "next", afterTurn: "0", state: "queued" },
      ],
    } satisfies ChatState);
    const next = reduceFrame(state, packet("error", { code, ref: "failed" }));
    expect(next.error).toContain(text);
    expect(next.pending.map((item) => item.state)).toEqual(["failed", "queued"]);
    expect(state.pending[0]?.state).toBe("sending");
    expect(reduceFrame(next, packet("notice", { code: "info", text: "ready" })).notice).toBe(
      "ready",
    );
    expect(reduceFrame(next, packet("pong", {}))).toBe(next);
    expect(reduceFrame(next, packet("error", { code: "resync_from_head", ref: "" }))).toBe(next);
  });

  it("clears reasoning snapshots, retains model metadata and preserves unknown progress parts", () => {
    const msg = { turn: "42", ordinal: -1 };
    let state = reduceFrame(
      freeze(initial()),
      packet("snapshot", { msg, seq: "1", kind: "model_start", text: "", model_ref: "model-a" }),
    );
    state = reduceFrame(
      freeze(state),
      packet("delta", { msg, seq: "2", kind: "reasoning", text: "old thinking" }),
    );
    state = reduceFrame(
      freeze(state),
      packet("snapshot", { msg, seq: "3", kind: "text", text: "answer", reasoning: "" }),
    );
    state = reduceFrame(
      freeze(state),
      packet("delta", { msg, seq: "4", kind: "future_part", text: "keep this data" }),
    );
    expect(state.live?.model).toBe("model-a");
    expect(state.live?.parts).toEqual([
      { ordinal: 0, part_type: "text", text: "answer" },
      { ordinal: 1, part_type: "future_part", text: "keep this data" },
    ]);
  });

  it("settles failed sends and preserves another active turn when an older terminal frame arrives", () => {
    const state = freeze({
      ...initial(),
      active: "43",
      pending: [
        { id: "old", text: "hello", afterTurn: "0", state: "running", turn: "42" },
        { id: "current", text: "next", afterTurn: "42", state: "running", turn: "43" },
      ],
    } satisfies ChatState);
    const next = reduceFrame(
      state,
      packet("msg.final", {
        msg: { turn: "42", ordinal: -1 },
        full_text: "partial",
        status: "failed",
        model_ref: "model-a",
      }),
    );
    expect(next.active).toBe("43");
    expect(next.pending.map((item) => item.state)).toEqual(["failed", "running"]);
    expect(next.error).toContain("response failed");
    expect(next.messages[0]?.model).toBe("model-a");
  });
});

import { describe, expect, it } from "vite-plus/test";

import type { ChatState } from "./chat";

import history from "../fixtures/server/history.json";
import streamed from "../fixtures/server/streamed.json";
import { isRecord } from "../shared/type-guards";
import { presentTurn } from "./activity";
import { reconcileHistory, reduceFrame } from "./chat";
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
    pending: [],
    active: null,
    cursor: "0",
    hasMore: false,
    loadingHistory: false,
    notice: null,
    error: null,
  };
}

describe("pure chat domain", () => {
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
});

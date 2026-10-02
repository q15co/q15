import { afterEach, describe, expect, expectTypeOf, it, vi } from "vite-plus/test";

import type { Transport } from "../application/ports";
import type { Pending } from "../domain/chat";

import { parseFrame, parsePage } from "../domain/protocol";
import aborted from "../fixtures/server/aborted.json";
import failed from "../fixtures/server/failed.json";
import history from "../fixtures/server/history.json";
import resumed from "../fixtures/server/resumed.json";
import streamed from "../fixtures/server/streamed.json";
import { frame } from "../infrastructure/envelope";
import { required } from "../testing/required";
import { ChatStore } from "./chat-store";
type HistoryMock = (before: string, signal?: AbortSignal) => Promise<ReturnType<typeof parsePage>>;
const empty = { turns: [], head_seq: "43", has_more: false };
const stores: ChatStore[] = [];
afterEach(() => {
  for (const store of stores.splice(0)) store.stop();
  vi.useRealTimers();
});
function setup(
  historyFn = vi.fn<HistoryMock>((_before: string) => Promise.resolve(parsePage(empty))),
) {
  const transport: Transport = {
    start: vi.fn<Transport["start"]>(),
    stop: vi.fn<Transport["stop"]>(),
    send: vi.fn<Transport["send"]>(),
    abort: vi.fn<Transport["abort"]>(),
    sync: vi.fn<Transport["sync"]>(),
    presence: vi.fn<Transport["presence"]>(),
  };
  const store = new ChatStore(transport, historyFn);
  stores.push(store);
  return { store, transport, historyFn };
}
const feed = (store: ChatStore, values: unknown[]) =>
  values.forEach((value) => store.consume(parseFrame(JSON.stringify(value))));
const event = (store: ChatStore, type: string, payload: unknown) =>
  store.consume(parseFrame(JSON.stringify(frame(type, payload))));
describe("chat state", () => {
  it("requires a turn for assigned sends and excludes it from waiting sends", () => {
    type Assigned = Extract<Pending, { state: "running" | "finished" | "stopped" }>;
    type Waiting = Extract<Pending, { state: "sending" | "accepted" | "queued" | "uncertain" }>;
    expectTypeOf<Assigned["turn"]>().toEqualTypeOf<string>();
    expectTypeOf<Waiting["turn"]>().toEqualTypeOf<undefined>();
  });
  it("renders a streamed draft and replaces it with canonical history identities", () => {
    const { store } = setup();
    feed(store, streamed);
    expect(store.getSnapshot().messages[0]?.parts.map((p) => p.part_type)).toEqual([
      "reasoning",
      "tool_call",
      "tool_result",
      "text",
    ]);
    expect(store.getSnapshot().active).toBeNull();
    feed(store, resumed);
    expect(store.getSnapshot().messages.map((m) => m.key)).toEqual(["42:0", "42:1"]);
    expect(store.getSnapshot().cursor).toBe("0");
    event(store, "ready", { cursor: "42", head_seq: "43", device_id: "a" });
    expect(store.getSnapshot().cursor).toBe("42");
  });
  it.each([aborted, failed])("retains the full terminal answer on abort and failure", (values) => {
    const { store } = setup();
    feed(store, [values]);
    expect(store.getSnapshot().messages[0]?.parts.at(-1)?.text).toBe(values.payload.full_text);
    expect(store.getSnapshot().active).toBeNull();
  });
  it("resets a model attempt and consumes reconnect snapshots without duplicating deltas", () => {
    const { store } = setup();
    feed(store, streamed.slice(0, 7));
    feed(store, [required(streamed[4])]);
    expect(store.getSnapshot().messages[0]?.parts.find((p) => p.part_type === "text")?.text).toBe(
      "answer",
    );
    feed(store, [required(streamed[2])]);
    expect(store.getSnapshot().messages[0]?.parts).toEqual([]);
    event(store, "snapshot", {
      msg: { turn: "42", ordinal: -1 },
      seq: "8",
      kind: "text",
      text: "resumed answer",
      reasoning: "resumed thinking",
    });
    expect(store.getSnapshot().messages[0]?.parts.map((p) => p.text)).toEqual([
      "resumed thinking",
      "resumed answer",
    ]);
  });
  it("correlates queue acceptance, allows stop before a turn starts, and preserves a failed draft", () => {
    const { store, transport } = setup();
    expect(store.send("hello")).toBe(true);
    const id = required(store.getSnapshot().pending[0]).id;
    event(store, "msg.status", { turn: "0", state: "accepted", queued: false, client_msg_id: id });
    store.abort();
    expect(transport.abort).toHaveBeenCalledWith("0");
    store.send("next");
    const next = required(store.getSnapshot().pending[1]).id;
    event(store, "msg.status", { turn: "42", state: "queued", queued: true, client_msg_id: next });
    expect(store.getSnapshot().pending[1]?.state).toBe("queued");
    vi.mocked(transport.send).mockImplementationOnce(() => {
      throw new Error("offline");
    });
    expect(store.send("draft")).toBe(false);
    expect(store.getSnapshot().error).toBe("offline");
  });
  it("retains completed tool activity across model loops while replacing retries of the current loop", () => {
    const { store } = setup();
    feed(store, streamed.slice(0, 8));
    const completed = required(store.getSnapshot().messages[0]).parts.map((p) =>
      p.part_type === "text" ? { ...p, disposition: "commentary" } : p,
    );
    const msg = { turn: "42", ordinal: -1 };
    event(store, "snapshot", { msg, seq: "10", kind: "model_start", text: "", loop_turn: 2 });
    expect(store.getSnapshot().messages[0]?.parts).toEqual(completed);
    event(store, "delta", { msg, seq: "11", kind: "text", text: "partial attempt" });
    event(store, "snapshot", { msg, seq: "12", kind: "model_start", text: "", loop_turn: 2 });
    expect(store.getSnapshot().messages[0]?.parts).toEqual(completed);
    event(store, "snapshot", {
      msg,
      seq: "13",
      kind: "text",
      text: "final answer",
      reasoning: "new reasoning",
    });
    expect(store.getSnapshot().messages[0]?.parts.slice(0, completed.length)).toEqual(completed);
    event(store, "msg.final", { msg, status: "completed", full_text: "final answer" });
    expect(
      store.getSnapshot().messages[0]?.parts.filter((p) => p.part_type === "tool_call"),
    ).toHaveLength(1);
    expect(
      store.getSnapshot().messages[0]?.parts.filter((p) => p.part_type === "tool_result"),
    ).toHaveLength(1);
    expect(store.getSnapshot().messages[0]?.parts.at(-1)).toMatchObject({
      text: "final answer",
      disposition: "final",
    });
  });
  it("rebuilds an earlier replayed model loop without duplicating completed tool work", () => {
    const { store, transport } = setup();
    store.start();
    feed(store, streamed.slice(0, 8));
    const msg = { turn: "42", ordinal: -1 };
    event(store, "snapshot", { msg, seq: "10", kind: "model_start", text: "", loop_turn: 2 });
    event(store, "delta", { msg, seq: "11", kind: "text", text: "second loop" });
    // A reconnect replays model_start and snapshots from the retained run log.
    const events = required(vi.mocked(transport.start).mock.calls[0])[0];
    events.connection("reconnecting");
    feed(store, [required(streamed[2])]);
    expect(store.getSnapshot().messages[0]?.parts).toEqual([]);
    feed(store, streamed.slice(3, 8));
    expect(
      store.getSnapshot().messages[0]?.parts.filter((p) => p.part_type === "tool_call"),
    ).toHaveLength(1);
    expect(
      store.getSnapshot().messages[0]?.parts.filter((p) => p.part_type === "tool_result"),
    ).toHaveLength(1);
  });
  it("treats an omitted loop counter as zero and retains its tools when loop one starts", () => {
    const { store } = setup();
    const msg = { turn: "42", ordinal: -1 };
    const call = { id: "zero-loop", name: "exec", arguments: '{"command":"pwd"}' };
    event(store, "snapshot", { msg, seq: "1", kind: "model_start", text: "" });
    event(store, "delta", { msg, seq: "2", kind: "tool_call", text: "", call });
    event(store, "delta", { msg, seq: "3", kind: "tool_result", text: "/workspace", call });
    event(store, "snapshot", { msg, seq: "4", kind: "model_start", text: "", loop_turn: 1 });
    expect(store.getSnapshot().messages[0]?.parts.map((p) => p.part_type)).toEqual([
      "tool_call",
      "tool_result",
    ]);
    // Replaying from loop zero clears the draft before tools are replayed.
    event(store, "snapshot", { msg, seq: "1", kind: "model_start", text: "" });
    expect(store.getSnapshot().messages[0]?.parts).toEqual([]);
  });
  it("marks unacknowledged sends uncertain when disconnected", () => {
    const { store, transport } = setup();
    store.start();
    store.send("hello");
    const events = required(vi.mocked(transport.start).mock.calls[0])[0];
    events.connection("reconnecting");
    expect(store.getSnapshot().pending[0]?.state).toBe("uncertain");
    store.stop();
  });
  it("settles a Stop during startup even when there is no turn.start event", () => {
    const { store, transport } = setup();
    store.send("cancel startup");
    event(store, "msg.status", {
      turn: "0",
      state: "accepted",
      queued: false,
      client_msg_id: required(store.getSnapshot().pending[0]).id,
    });
    store.abort();
    expect(transport.abort).toHaveBeenCalledWith("0");
    feed(store, [aborted]);
    expect(store.getSnapshot().active).toBeNull();
    expect(store.getSnapshot().pending[0]?.state).toBe("stopped");
  });
  it("settles an aborted local send even when the backend never persists its turn", async () => {
    const { store } = setup();
    store.send("cancel me");
    const first = required(store.getSnapshot().pending[0]).id;
    event(store, "turn.start", { turn: "42", msg: { turn: "42", ordinal: -1 } });
    // Acceptance can arrive after the first streaming event.
    event(store, "msg.status", {
      turn: "42",
      state: "accepted",
      queued: false,
      client_msg_id: first,
    });
    store.send("next");
    const next = required(store.getSnapshot().pending[1]).id;
    event(store, "msg.status", { turn: "42", state: "queued", queued: true, client_msg_id: next });
    feed(store, [aborted]);
    await store.loadHistory(false);
    expect(store.getSnapshot().pending.map((p) => p.state)).toEqual(["stopped", "queued"]);
    expect(store.getSnapshot().active).toBeNull();
    event(store, "turn.start", { turn: "43", msg: { turn: "43", ordinal: -1 } });
    event(store, "msg.final", {
      msg: { turn: "43", ordinal: -1 },
      full_text: "done",
      status: "completed",
    });
    expect(store.getSnapshot().pending.map((p) => p.state)).toEqual(["stopped", "finished"]);
    expect(store.getSnapshot().active).toBeNull();
  });
  it("retries canonical history when the terminal frame precedes persistence", async () => {
    vi.useFakeTimers();
    const fetch = vi
      .fn<HistoryMock>()
      .mockResolvedValueOnce(parsePage(empty))
      .mockResolvedValue(parsePage(history));
    const { store } = setup(fetch);
    store.send("hello");
    event(store, "turn.start", { turn: "42", msg: { turn: "42", ordinal: -1 } });
    feed(store, [required(streamed.at(-1))]);
    await vi.advanceTimersByTimeAsync(0);
    expect(store.getSnapshot().pending[0]?.state).toBe("finished");
    expect(store.getSnapshot().messages[0]?.key).toBe("42:-1");
    await vi.advanceTimersByTimeAsync(1000);
    expect(store.getSnapshot().pending).toEqual([]);
    expect(store.getSnapshot().messages.map((m) => m.key)).toEqual(["42:0", "42:1"]);
  });
  it("prepends older pages, deduplicates history, and finds a message beyond the live window", async () => {
    const older = {
      ...history,
      turns: [{ ...required(history.turns[0]), seq: "40" }],
      has_more: false,
    };
    const fetch = vi.fn<HistoryMock>((before: string) =>
      Promise.resolve(parsePage(before === "0" ? history : older)),
    );
    const { store } = setup(fetch);
    await store.loadHistory(false);
    expect(await store.findMessage("40", 0)).toBe("40:0");
    expect(fetch).toHaveBeenLastCalledWith("42", undefined);
    expect(store.getSnapshot().messages.map((m) => m.key)).toEqual([
      "40:0",
      "40:1",
      "42:0",
      "42:1",
    ]);
    await store.loadHistory(false);
    expect(store.getSnapshot().messages).toHaveLength(4);
    expect(store.getSnapshot().hasMore).toBe(false);
  });
  it("resyncs from completed history rather than an allocated live head", async () => {
    const { store, transport } = setup(
      vi.fn<HistoryMock>(() => Promise.resolve(parsePage(history))),
    );
    event(store, "error", { code: "resync_from_head", ref: "", head_seq: "43" });
    await vi.waitFor(() => expect(transport.sync).toHaveBeenCalledWith("42"));
    expect(store.getSnapshot().cursor).toBe("42");
  });
});

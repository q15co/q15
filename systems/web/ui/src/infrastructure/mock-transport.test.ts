import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { TransportEvents } from "../application/ports";
import type { ServerFrame } from "../domain/protocol";

import { MockTransport } from "./mock-transport";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

function setup() {
  const frames: ServerFrame[] = [];
  const connection = vi.fn<TransportEvents["connection"]>();
  const transport = new MockTransport();
  transport.start({
    frame: (value) => {
      frames.push(value);
    },
    connection,
    error: () => {},
  });
  return { transport, frames, connection };
}

describe("offline preview transport", () => {
  it("replays canonical history and runs queued sends in order with fresh event IDs", async () => {
    const { transport, frames, connection } = setup();
    expect(connection).toHaveBeenCalledWith("connected");
    expect(frames.at(-1)).toMatchObject({ type: "ready", payload: { cursor: "42" } });
    transport.send("first", "first-id");
    transport.send("second", "second-id");
    await vi.advanceTimersByTimeAsync(0);
    expect(
      frames.filter((value) => value.type === "msg.status").map((value) => value.payload),
    ).toEqual([
      { turn: "43", state: "accepted", client_msg_id: "first-id", queued: false },
      { turn: "43", state: "queued", client_msg_id: "second-id", queued: true },
    ]);
    await vi.runAllTimersAsync();
    const finished = frames.filter(
      (value): value is Extract<ServerFrame, { type: "msg.final" }> =>
        value.type === "msg.final" && value.id.startsWith("preview:"),
    );
    expect(finished.map((value) => value.payload.msg.turn)).toEqual(["43", "44"]);
    expect(new Set(finished.map((value) => value.id)).size).toBe(2);
    const page = await transport.history("0");
    expect(page.head_seq).toBe("44");
    expect(page.turns.slice(0, 2).map((turn) => turn.messages[0]?.parts[0]?.text)).toEqual([
      "second",
      "first",
    ]);
    expect((await transport.history("43")).turns.map((turn) => turn.seq)).toEqual(["42"]);
    transport.presence();
    transport.stop();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("aborts the active run, advances the queue and stops all remaining work", async () => {
    const { transport, frames } = setup();
    transport.send("cancel me", "cancel-id");
    transport.send("next", "next-id");
    await vi.advanceTimersByTimeAsync(800);
    transport.abort();
    expect(frames.at(-1)).toMatchObject({
      type: "msg.final",
      payload: { status: "aborted", msg: { turn: "43" } },
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(frames.at(-1)).toMatchObject({
      type: "msg.status",
      payload: { client_msg_id: "next-id", queued: false },
    });
    transport.stop();
    const count = frames.length;
    await vi.runAllTimersAsync();
    transport.sync();
    expect(frames).toHaveLength(count);
    transport.start({
      frame: (value) => {
        frames.push(value);
      },
      connection: () => {},
      error: () => {},
    });
    expect(frames.at(-1)).toMatchObject({ type: "ready" });
    transport.stop();
  });
});

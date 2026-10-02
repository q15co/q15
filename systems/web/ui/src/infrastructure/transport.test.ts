import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { TransportEvents } from "../application/ports";
import type { ClientFrame } from "../domain/protocol";
import type { SocketLike } from "./transport";

import { parseClientFrame } from "../domain/protocol";
import { frame } from "../infrastructure/envelope";
import { required } from "../testing/required";
import { SocketTransport } from "./transport";

class FakeSocket implements SocketLike {
  readyState = 0;
  onopen: SocketLike["onopen"] = null;
  onclose: SocketLike["onclose"] = null;
  onerror: SocketLike["onerror"] = null;
  onmessage: SocketLike["onmessage"] = null;
  sent: ClientFrame[] = [];
  send(data: string) {
    this.sent.push(parseClientFrame(data));
  }
  close() {
    this.readyState = 3;
    this.onclose?.(new CloseEvent("close"));
  }
  open() {
    this.readyState = 1;
    this.onopen?.(new Event("open"));
  }
  receive(type: string, payload: unknown) {
    this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(frame(type, payload)) }));
  }
}

function setup() {
  const sockets: FakeSocket[] = [];
  const transport = new SocketTransport("ws://localhost/ws", () => {
    const socket = new FakeSocket();
    sockets.push(socket);
    return socket;
  });
  const events = {
    frame: vi.fn<TransportEvents["frame"]>(),
    connection: vi.fn<TransportEvents["connection"]>(),
    error: vi.fn<TransportEvents["error"]>(),
  };
  let cursor = "41";
  transport.start(events, () => cursor);
  const socket = required(sockets[0]);
  socket.open();
  socket.receive("ready", { cursor: "42", head_seq: "43", device_id: "device-1" });
  return {
    transport,
    socket,
    sockets,
    events,
    cursor: (value: string) => {
      cursor = value;
    },
  };
}
afterEach(() => vi.useRealTimers());
describe("socket transport", () => {
  it("connects with hello then sends, queues through the server, aborts and syncs", () => {
    const { transport, socket, events } = setup();
    expect(socket.sent[0]).toMatchObject({ v: 1, type: "hello", payload: { cursor: "41" } });
    transport.send("hello", "first");
    transport.send("next", "second");
    transport.abort("42");
    transport.sync("41");
    expect(socket.sent.slice(-4).map((f) => [f.v, f.type, f.payload])).toEqual([
      [1, "msg.send", { text: "hello", client_msg_id: "first" }],
      [1, "msg.send", { text: "next", client_msg_id: "second" }],
      [1, "msg.abort", { turn: "42" }],
      [1, "sync", { cursor: "41" }],
    ]);
    expect(events.connection).toHaveBeenCalledWith("connected");
    transport.stop();
  });
  it("reconnects with the consumed durable cursor and never resubmits an uncertain send", () => {
    vi.useFakeTimers();
    const { transport, socket, sockets, cursor } = setup();
    transport.send("uncertain", "client-1");
    cursor("9007199254740992");
    socket.close();
    expect(() => transport.send("offline", "client-2")).toThrow(/reconnect/u);
    vi.advanceTimersByTime(1000);
    required(sockets[1]).open();
    expect(required(sockets[1]).sent).toHaveLength(1);
    expect(required(sockets[1]).sent[0]).toMatchObject({
      type: "hello",
      payload: { cursor: "9007199254740992" },
    });
    transport.stop();
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(2);
  });
  it("retries a stalled handshake and surfaces malformed frames", () => {
    vi.useFakeTimers();
    const { transport, socket, events, sockets } = setup();
    socket.onmessage?.(new MessageEvent("message", { data: JSON.stringify({ v: 2 }) }));
    expect(events.error).toHaveBeenCalled();
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
    transport.start(events, () => "0");
    vi.advanceTimersByTime(16_000);
    expect(sockets).toHaveLength(3);
    transport.stop();
  });
});

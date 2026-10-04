import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { TransportEvents } from "../application/ports";
import type { ClientFrame } from "../domain/protocol";
import type { Frame } from "../generated/protocol";
import type { SocketLike } from "./transport";

import { parseClientFrame } from "../domain/protocol";
import { MaxClientFrameBytes } from "../generated/protocol";
import { frame } from "../infrastructure/envelope";
import { required } from "../testing/required";
import { proofHeaders, sessionKey } from "../testing/session-key";
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

async function setup() {
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
  await vi.waitFor(() => expect(socket.sent).toHaveLength(1));
  socket.receive("ready", { cursor: "42", head_seq: "43", device_id: "device-1" });
  await vi.waitFor(() => expect(events.connection).toHaveBeenCalledWith("connected"));
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
beforeEach(sessionKey);

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
describe("socket transport", () => {
  it("reports the encoded wire limit without sending or dropping the socket", async () => {
    const { transport, socket, events } = await setup();
    const before = socket.sent.length;
    transport.send("x".repeat(MaxClientFrameBytes), "large");
    await vi.waitFor(() =>
      expect(events.frame).toHaveBeenLastCalledWith(
        expect.objectContaining({
          type: "error",
          payload: { code: "message_too_large", ref: "large" },
        }),
      ),
    );
    expect(socket.sent).toHaveLength(before);
    expect(socket.readyState).toBe(1);
    transport.send("small", "next");
    await vi.waitFor(() => expect(socket.sent.at(-1)?.id).toBe("next"));
    transport.stop();
  });

  it("refreshes exhausted content keys after accepted sends drain", async () => {
    vi.useFakeTimers();
    const { transport, socket, events, sockets } = await setup();
    let release: ((value: Frame) => void) | undefined;
    let pending: Frame | undefined;
    vi.spyOn(transport.content, "wrap").mockImplementationOnce((value) => {
      pending = value;
      return new Promise((resolve) => {
        release = resolve;
      });
    });
    transport.send("accepted", "pending");
    await vi.waitFor(() => expect(pending).toBeDefined());
    const refresh = vi.spyOn(transport.content, "needsRefresh", "get").mockReturnValue(true);
    socket.receive("notice", { code: "info", text: "last frame" });
    await vi.waitFor(() => expect(events.connection).toHaveBeenLastCalledWith("reconnecting"));
    expect(socket.readyState).toBe(1);
    expect(() => transport.send("later", "later")).toThrow("reconnect");
    socket.receive("ready", { cursor: "42", head_seq: "43", device_id: "old" });
    await vi.waitFor(() =>
      expect(events.frame).toHaveBeenLastCalledWith(expect.objectContaining({ type: "ready" })),
    );
    expect(events.connection).toHaveBeenLastCalledWith("reconnecting");
    expect(() => transport.send("later", "later")).toThrow("reconnect");
    required(release)(required(pending));
    await vi.waitFor(() => expect(socket.readyState).toBe(3));
    expect(socket.sent.filter((value) => value.type === "msg.send")).toHaveLength(1);
    refresh.mockReturnValue(false);
    vi.advanceTimersByTime(1000);
    const next = required(sockets[1]);
    next.open();
    next.receive("ready", { cursor: "42", head_seq: "43", device_id: "new" });
    await vi.waitFor(() => expect(events.connection).toHaveBeenLastCalledWith("connected"));
    expect(next.sent.filter((value) => value.type === "msg.send")).toHaveLength(0);
    transport.stop();
  });
  it("stops reconnecting when an open session is revoked or expires", async () => {
    vi.useFakeTimers();
    const { transport, socket, sockets, events } = await setup();
    socket.onclose?.(new CloseEvent("close", { code: 4401 }));
    expect(events.connection).toHaveBeenLastCalledWith("unauthorized");
    expect(events.error).toHaveBeenCalledWith(expect.stringContaining("Sign in again"));
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
    transport.stop();
  });

  it("checks HTTP session status when a handshake hides the 401", async () => {
    vi.useFakeTimers();
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 401 })),
    );
    const socket = new FakeSocket();
    const events = {
      frame: vi.fn<TransportEvents["frame"]>(),
      connection: vi.fn<TransportEvents["connection"]>(),
      error: vi.fn<TransportEvents["error"]>(),
    };
    const transport = new SocketTransport("ws://localhost/ws", () => socket);
    transport.start(events, () => "0");
    socket.close();
    await vi.waitFor(() => expect(events.connection).toHaveBeenLastCalledWith("unauthorized"));
    expect(fetch).toHaveBeenCalledWith("/auth/session", {
      credentials: "same-origin",
      cache: "no-store",
      headers: proofHeaders,
    });
    expect(vi.getTimerCount()).toBe(0);
  });

  it("keeps network failures retryable and ignores a stale session probe", async () => {
    vi.useFakeTimers();
    const socket = new FakeSocket();
    const status = vi
      .fn<() => Promise<number>>()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue(401);
    const events = {
      frame: vi.fn<TransportEvents["frame"]>(),
      connection: vi.fn<TransportEvents["connection"]>(),
      error: vi.fn<TransportEvents["error"]>(),
    };
    const transport = new SocketTransport("ws://localhost/ws", () => socket, status);
    transport.start(events, () => "0");
    socket.close();
    await Promise.resolve();
    expect(events.connection).toHaveBeenLastCalledWith("reconnecting");
    socket.close();
    transport.stop();
    await Promise.resolve();
    expect(events.connection).not.toHaveBeenCalledWith("unauthorized");
  });
  it("reports an unreadable frame and consumes the next valid frame on the same socket", async () => {
    const { transport, socket, events } = await setup();
    vi.spyOn(transport.content, "open").mockRejectedValueOnce(new Error("bad tag"));
    socket.receive("notice", { code: "outbound", text: "unreadable" });
    await vi.waitFor(() =>
      expect(events.error).toHaveBeenCalledWith(expect.stringContaining("could not be decrypted")),
    );
    expect(socket.readyState).toBe(1);
    socket.receive("notice", { code: "outbound", text: "still connected" });
    await vi.waitFor(() =>
      expect(events.frame).toHaveBeenLastCalledWith(
        expect.objectContaining({ payload: { code: "outbound", text: "still connected" } }),
      ),
    );
    transport.stop();
  });
  it("accepts content keys before replay and refuses a failed key exchange", async () => {
    const { transport, socket, events } = await setup();
    const accept = vi.spyOn(transport.content, "accept");
    socket.receive("key", { binding: "binding", public_key: "public", channel_id: "channel" });
    await vi.waitFor(() => expect(accept).toHaveBeenCalled());
    accept.mockRejectedValueOnce(new Error("bad key"));
    socket.receive("key", { binding: "binding", public_key: "public", channel_id: "channel" });
    await vi.waitFor(() => expect(events.connection).toHaveBeenLastCalledWith("offline"));
    transport.stop();
  });

  it("keeps pending encrypted sends out of replacement sockets", async () => {
    const { transport, socket, events } = await setup();
    let finish: ((value: Frame) => void) | undefined;
    vi.spyOn(transport.content, "wrap").mockImplementationOnce(
      (value) =>
        new Promise<Frame>((resolve) => {
          finish = () => resolve(value);
        }),
    );
    transport.send("pending", "pending-client");
    await vi.waitFor(() => expect(finish).toBeDefined());
    socket.close();
    finish?.(frame("msg.send", {}));
    await vi.waitFor(() =>
      expect(events.frame).toHaveBeenLastCalledWith(
        expect.objectContaining({
          type: "error",
          payload: { code: "seal_failed", ref: "pending-client" },
        }),
      ),
    );
    expect(socket.sent.filter((f) => f.type === "msg.send")).toHaveLength(0);
    transport.stop();
  });
  it("uses proofs on secure native sockets and reports a refused content offer", async () => {
    const socket = new FakeSocket();
    const create = vi.fn<(address: string, protocols: string[]) => SocketLike>(function (
      _address: string,
      _protocols: string[],
    ) {
      return socket;
    });
    vi.stubGlobal("WebSocket", create);
    vi.stubGlobal("location", {
      protocol: "https:",
      host: "chat.example",
      origin: "https://chat.example",
    });
    const events = {
      frame: vi.fn<TransportEvents["frame"]>(),
      connection: vi.fn<TransportEvents["connection"]>(),
      error: vi.fn<TransportEvents["error"]>(),
    };
    const transport = new SocketTransport();
    transport.start(events, () => "0");
    await vi.waitFor(() => expect(create).toHaveBeenCalled());
    expect(create.mock.calls[0]?.[0]).toBe("wss://chat.example/ws");
    expect(create.mock.calls[0]?.[1][0]).toBe("q15-auth");
    vi.spyOn(transport.content, "offer").mockRejectedValueOnce(new Error("no key"));
    socket.open();
    await vi.waitFor(() => expect(events.connection).toHaveBeenLastCalledWith("unauthorized"));
    transport.stop();
  });
  it("connects with hello then sends, queues through the server, aborts and syncs", async () => {
    const { transport, socket, events } = await setup();
    expect(socket.sent[0]).toMatchObject({ v: 2, type: "hello", payload: { cursor: "41" } });
    transport.send("hello", "first");
    transport.send("next", "second");
    await vi.waitFor(() => expect(socket.sent).toHaveLength(4));
    transport.abort("42");
    transport.sync("41");
    expect(socket.sent.slice(-4).map((f) => [f.v, f.type, f.payload])).toEqual([
      [2, "msg.send", { text: "hello", client_msg_id: "first" }],
      [2, "msg.send", { text: "next", client_msg_id: "second" }],
      [2, "msg.abort", { turn: "42" }],
      [2, "sync", { cursor: "41" }],
    ]);
    expect(events.connection).toHaveBeenCalledWith("connected");
    transport.stop();
  });
  it("reconnects with the consumed durable cursor and never resubmits an uncertain send", async () => {
    vi.useFakeTimers();
    const { transport, socket, sockets, cursor } = await setup();
    transport.send("uncertain", "client-1");
    await vi.waitFor(() => expect(socket.sent).toHaveLength(3));
    cursor("9007199254740992");
    socket.close();
    expect(() => transport.send("offline", "client-2")).toThrow(/reconnect/u);
    vi.advanceTimersByTime(1000);
    required(sockets[1]).open();
    await vi.waitFor(() => expect(required(sockets[1]).sent).toHaveLength(1));
    expect(required(sockets[1]).sent).toHaveLength(1);
    expect(required(sockets[1]).sent[0]).toMatchObject({
      type: "hello",
      payload: { cursor: "9007199254740992" },
    });
    transport.stop();
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(2);
  });
  it("retries a stalled handshake and surfaces malformed frames", async () => {
    vi.useFakeTimers();
    const { transport, socket, events, sockets } = await setup();
    socket.onmessage?.(new MessageEvent("message", { data: JSON.stringify({ v: 1 }) }));
    await vi.waitFor(() => expect(events.error).toHaveBeenCalled());
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
    transport.start(events, () => "0");
    vi.advanceTimersByTime(16_000);
    expect(sockets).toHaveLength(3);
    transport.stop();
  });

  it("acknowledges sequenced events, skips error acknowledgements and reports presence", async () => {
    const { transport, socket, events } = await setup();
    transport.presence(false);
    expect(socket.sent.at(-1)).toMatchObject({ type: "presence", payload: { fg: false } });
    socket.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({ ...frame("notice", { code: "info", text: "hello" }), seq: "100" }),
      }),
    );
    await vi.waitFor(() =>
      expect(socket.sent.at(-1)).toMatchObject({ type: "msg.ack", payload: { seq: "100" } }),
    );
    const count = socket.sent.length;
    socket.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({
          ...frame("error", { code: "invalid_message", ref: "send" }),
          seq: "101",
        }),
      }),
    );
    await vi.waitFor(() =>
      expect(events.frame).toHaveBeenLastCalledWith(expect.objectContaining({ type: "error" })),
    );
    expect(socket.sent).toHaveLength(count);
    socket.readyState = 3;
    transport.send("keep my draft", "send");
    await vi.waitFor(() =>
      expect(events.frame).toHaveBeenLastCalledWith(
        expect.objectContaining({ type: "error", payload: { code: "seal_failed", ref: "send" } }),
      ),
    );
    transport.stop();
    transport.presence(true);
    expect(socket.sent).toHaveLength(count);
  });

  it("retries a socket constructor failure and ignores late close notifications after stop", () => {
    vi.useFakeTimers();
    const socket = new FakeSocket();
    const create = vi
      .fn<(address: string) => SocketLike>()
      .mockImplementationOnce(() => {
        throw new Error("connection refused");
      })
      .mockReturnValue(socket);
    const events = {
      frame: vi.fn<TransportEvents["frame"]>(),
      connection: vi.fn<TransportEvents["connection"]>(),
      error: vi.fn<TransportEvents["error"]>(),
    };
    const transport = new SocketTransport("ws://localhost/ws", create);
    transport.start(events, () => "0");
    expect(events.connection).toHaveBeenLastCalledWith("reconnecting");
    vi.advanceTimersByTime(1000);
    expect(create).toHaveBeenCalledTimes(2);
    expect(() => transport.send("too soon", "send")).toThrow("Wait for chat");
    const close = required(socket.onclose);
    socket.onerror?.(new Event("error"));
    transport.stop();
    close(new CloseEvent("close"));
    vi.advanceTimersByTime(60_000);
    expect(create).toHaveBeenCalledTimes(2);
    expect(vi.getTimerCount()).toBe(0);
    expect(() => transport.abort("42")).toThrow("disconnected");
  });

  it("closes incompatible binary frames without retrying them", async () => {
    vi.useFakeTimers();
    const { transport, socket, events, sockets } = await setup();
    socket.onmessage?.(new MessageEvent("message", { data: new Uint8Array([1, 2, 3]) }));
    await vi.waitFor(() =>
      expect(events.error).toHaveBeenCalledWith("Expected a text chat frame."),
    );
    expect(events.connection).toHaveBeenLastCalledWith("offline");
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
    transport.stop();
  });
});

it("attaches asynchronously signed sockets and discards sockets from stopped generations", async () => {
  const socket = new FakeSocket();
  const events = {
    frame: vi.fn<TransportEvents["frame"]>(),
    connection: vi.fn<TransportEvents["connection"]>(),
    error: vi.fn<TransportEvents["error"]>(),
  };
  const transport = new SocketTransport("ws://localhost/ws", () => Promise.resolve(socket));
  transport.start(events, () => "0");
  await Promise.resolve();
  socket.open();
  socket.receive("ready", { cursor: "0", head_seq: "0", device_id: "device" });
  await vi.waitFor(() => expect(events.connection).toHaveBeenLastCalledWith("connected"));
  transport.stop();
  const pending = new FakeSocket();
  const stopped = new SocketTransport("ws://localhost/ws", () => Promise.resolve(pending));
  stopped.start(events, () => "0");
  stopped.stop();
  await Promise.resolve();
  expect(pending.readyState).toBe(3);
  const failed = new SocketTransport("ws://localhost/ws", () =>
    Promise.reject(new Error("No key")),
  );
  failed.start(events, () => "0");
  await Promise.resolve();
  expect(events.connection).toHaveBeenLastCalledWith("unauthorized");
  failed.start(events, () => "0");
  failed.stop();
  await Promise.resolve();
  expect(events.connection).toHaveBeenLastCalledWith("connecting");
});

vi.mock("./seal", async () => {
  const { PlainContent, hasContent } = await import("../testing/content");
  return { ContentSession: PlainContent, hasContent };
});

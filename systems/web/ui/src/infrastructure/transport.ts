import type { Transport, TransportEvents } from "../application/ports";
import type { Frame } from "../generated/protocol";

import { parseFrameValue, parseWireFrame } from "../domain/protocol";
import { MaxClientFrameBytes } from "../generated/protocol";
import { clientFrame, frame } from "./envelope";
import { authenticatedFetch, requestProof } from "./proof";
import { ContentSession, hasContent } from "./seal";

export interface SocketLike {
  readyState: number;
  onopen: ((event: Event) => void) | null;
  onclose: ((event: CloseEvent) => void) | null;
  onerror: ((event: Event) => void) | null;
  onmessage: ((event: MessageEvent) => void) | null;
  send(data: string): void;
  close(): void;
}

export class SocketTransport implements Transport {
  private socket?: SocketLike;
  private events?: TransportEvents;
  private cursor = () => "0";
  private stopped = true;
  private ready = false;
  private refreshing = false;
  private attempts = 0;
  private timer?: ReturnType<typeof setTimeout>;
  private generation = 0;
  private incoming: Promise<void> = Promise.resolve();
  private outgoing: Promise<void> = Promise.resolve();
  private handshake?: ReturnType<typeof setTimeout>;

  constructor(
    private readonly url = `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/ws`,
    private readonly createSocket: (url: string) => SocketLike | Promise<SocketLike> = async (
      address,
    ) => {
      const target = new URL(address);
      const origin = `${target.protocol === "wss:" ? "https:" : "http:"}//${target.host}`;
      const proof = await requestProof("GET", `${target.pathname}${target.search}`, origin, "ws");
      return new WebSocket(address, ["q15-auth", `q15-proof.${proof}`]);
    },
    private readonly sessionStatus = async () => (await authenticatedFetch("/auth/session")).status,
    readonly content = new ContentSession(),
  ) {}

  start(events: TransportEvents, cursor: () => string) {
    this.stop();
    this.events = events;
    this.cursor = cursor;
    this.stopped = false;
    this.attempts = 0;
    this.connect();
  }

  stop() {
    this.stopped = true;
    this.generation++;
    this.ready = false;
    this.refreshing = false;
    this.content.reset();
    clearTimeout(this.timer);
    clearTimeout(this.handshake);
    if (this.socket) {
      this.socket.onopen = this.socket.onclose = this.socket.onerror = this.socket.onmessage = null;
      this.socket.close();
    }
  }

  private connect() {
    if (this.stopped) return;
    this.ready = false;
    this.events?.connection(this.attempts > 0 ? "reconnecting" : "connecting");
    let socket: SocketLike;
    try {
      const generation = this.generation;
      const created = this.createSocket(this.url);
      if (created instanceof Promise) {
        void this.accept(created, generation);
        return;
      }
      socket = created;
    } catch {
      this.retry();
      return;
    }
    this.attach(socket);
  }

  private async accept(created: Promise<SocketLike>, generation: number) {
    try {
      const socket = await created;
      if (this.stopped || generation !== this.generation) {
        socket.close();
        return;
      }
      this.attach(socket);
    } catch {
      if (!this.stopped && generation === this.generation) this.expired();
    }
  }

  private attach(socket: SocketLike) {
    this.socket = socket;
    this.refreshing = false;
    // A stalled handshake must not leave the composer waiting indefinitely.
    this.handshake = setTimeout(() => socket.close(), 15_000);
    socket.onopen = () => {
      void this.offer(socket, this.generation);
    };
    socket.onmessage = (event) => {
      const generation = this.generation;
      this.incoming = this.incoming
        .then(() => this.receive(event, socket, generation))
        .catch((error: unknown) => {
          if (generation !== this.generation || socket !== this.socket || this.stopped) return;
          this.events?.error(error instanceof Error ? error.message : "Invalid chat frame.");
          this.stop();
          this.events?.connection("offline");
        });
    };
    socket.onerror = () => socket.close();
    socket.onclose = (event) => {
      clearTimeout(this.handshake);
      this.ready = false;
      this.generation++;
      this.content.reset();
      if (event.code === 4401) {
        this.expired();
        return;
      }
      this.retry();
      void this.checkSession(socket);
    };
  }

  private expired() {
    this.stop();
    this.events?.connection("unauthorized");
    this.events?.error("Your session expired or was revoked. Sign in again to continue.");
  }

  private async checkSession(socket: SocketLike) {
    try {
      const status = await this.sessionStatus();
      if (!this.stopped && socket === this.socket && status === 401) this.expired();
    } catch {
      // An unavailable network follows the existing reconnect backoff.
    }
  }

  private retry() {
    if (this.stopped) return;
    this.events?.connection("reconnecting");
    const delay = Math.min(1000 * 2 ** this.attempts++, 30_000);
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.connect(), delay);
  }

  private write(value: Frame) {
    if (this.socket?.readyState !== 1)
      throw new Error("Chat is disconnected. Your draft is still here.");
    const data = JSON.stringify(value);
    if (new TextEncoder().encode(data).length > MaxClientFrameBytes) {
      this.events?.frame(
        parseFrameValue(frame("error", { code: "message_too_large", ref: value.id })),
      );
      return;
    }
    this.socket.send(data);
  }

  send(text: string, clientID: string) {
    if (!this.ready) throw new Error("Wait for chat to reconnect. Your draft is still here.");
    // Never replay a send after reconnect: client_msg_id is correlation, not idempotency.
    const socket = this.socket;
    const generation = this.generation;
    this.outgoing = this.outgoing
      .then(() => this.sendSealed(text, clientID, socket, generation))
      .catch(() => {
        if (!this.stopped)
          this.events?.frame(
            parseFrameValue(frame("error", { code: "seal_failed", ref: clientID })),
          );
      });
  }

  private async sendSealed(
    text: string,
    clientID: string,
    socket: SocketLike | undefined,
    generation: number,
  ) {
    const sealed = await this.content.wrap(
      clientFrame("msg.send", { client_msg_id: clientID, text }, clientID),
    );
    if (generation !== this.generation || socket !== this.socket || this.stopped)
      throw new Error("Chat disconnected before the message was sent.");
    this.write(sealed);
    this.refreshContent();
  }

  private refreshContent() {
    if (!this.ready || !this.content.needsRefresh) return;
    this.ready = false;
    this.refreshing = true;
    this.events?.connection("reconnecting");
    const socket = this.socket;
    const generation = this.generation;
    // Drain accepted sends before retiring the key; uncertain sends are never replayed.
    void this.outgoing.then(() =>
      !this.stopped && generation === this.generation && socket === this.socket
        ? socket?.close()
        : undefined,
    );
  }

  private async offer(socket: SocketLike, generation: number) {
    try {
      const hello = await this.content.offer(this.cursor());
      if (!this.stopped && generation === this.generation && socket === this.socket)
        this.write(clientFrame("hello", hello));
    } catch {
      if (socket === this.socket && !this.stopped) this.expired();
    }
  }

  private async receive(event: MessageEvent, socket: SocketLike, generation: number) {
    const current = () => generation === this.generation && socket === this.socket && !this.stopped;
    if (!current()) return;
    if (typeof event.data !== "string") throw new Error("Expected a text chat frame.");
    let wire = parseWireFrame(event.data);
    if (wire.type === "key") {
      await this.content.accept(wire.payload);
      return;
    }
    if (hasContent(wire.type)) {
      try {
        wire = await this.content.open(wire);
      } catch {
        if (current())
          this.events?.error("This content could not be decrypted. Reconnect to try again.");
        return;
      }
    }
    if (!current()) return;
    const value = parseFrameValue(wire);
    if (value.type === "ready" && !this.refreshing) {
      this.ready = true;
      this.attempts = 0;
      clearTimeout(this.handshake);
      this.events?.connection("connected");
    }
    this.events?.frame(value);
    if (value.type === "ready") this.write(clientFrame("msg.status", {}));
    if (value.seq !== "0" && value.type !== "error")
      this.write(clientFrame("msg.ack", { seq: value.seq }));
    this.refreshContent();
  }

  abort(turn: string) {
    this.write(clientFrame("msg.abort", { turn }));
  }
  sync(cursor: string) {
    this.write(clientFrame("sync", { cursor }));
  }
  presence(foreground: boolean) {
    if (this.ready) this.write(clientFrame("presence", { fg: foreground }));
  }
}

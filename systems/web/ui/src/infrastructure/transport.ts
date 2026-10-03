import type { Transport, TransportEvents } from "../application/ports";
import type { ClientFrame } from "../domain/protocol";

import { parseFrame } from "../domain/protocol";
import { clientFrame } from "./envelope";
import { authenticatedFetch, requestProof } from "./proof";

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
  private attempts = 0;
  private timer?: ReturnType<typeof setTimeout>;
  private generation = 0;
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
    // A stalled handshake must not leave the composer waiting indefinitely.
    this.handshake = setTimeout(() => socket.close(), 15_000);
    socket.onopen = () => this.write(clientFrame("hello", { cursor: this.cursor() }));
    socket.onmessage = (event) => {
      try {
        if (typeof event.data !== "string") throw new Error("Expected a text chat frame.");
        const value = parseFrame(event.data);
        if (value.type === "ready") {
          this.ready = true;
          this.attempts = 0;
          clearTimeout(this.handshake);
          this.events?.connection("connected");
        }
        this.events?.frame(value);
        if (value.type === "ready") this.write(clientFrame("msg.status", {}));
        // Event acknowledgements are separate from the durable replay cursor.
        if (value.seq !== "0" && value.type !== "error")
          this.write(clientFrame("msg.ack", { seq: value.seq }));
      } catch (error) {
        this.events?.error(error instanceof Error ? error.message : "Invalid chat frame.");
        // Stop on a contract mismatch; retrying the same incompatible server cannot fix it.
        this.stop();
        this.events?.connection("offline");
      }
    };
    socket.onerror = () => socket.close();
    socket.onclose = (event) => {
      clearTimeout(this.handshake);
      this.ready = false;
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

  private write(value: ClientFrame) {
    if (this.socket?.readyState !== 1)
      throw new Error("Chat is disconnected. Your draft is still here.");
    this.socket.send(JSON.stringify(value));
  }

  send(text: string, clientID: string) {
    if (!this.ready) throw new Error("Wait for chat to reconnect. Your draft is still here.");
    // Never replay a send after reconnect: client_msg_id is correlation, not idempotency.
    this.write(clientFrame("msg.send", { client_msg_id: clientID, text }, clientID));
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

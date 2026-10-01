import { frame, parseFrame } from "./protocol";
import type { ServerFrame } from "./protocol";
import type { Frame } from "./generated/protocol";

export type Connection = "connecting" | "connected" | "reconnecting" | "offline";
export interface TransportEvents {
  frame: (value: ServerFrame) => void;
  connection: (value: Connection) => void;
  error: (message: string) => void;
}
export interface Transport {
  start: (events: TransportEvents, cursor: () => string) => void;
  stop: () => void;
  send: (text: string, clientID: string) => void;
  abort: (turn: string) => void;
  sync: (cursor: string) => void;
  presence: (foreground: boolean) => void;
}
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
  private handshake?: ReturnType<typeof setTimeout>;

  constructor(
    private readonly url = `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/ws`,
    private readonly createSocket: (url: string) => SocketLike = (url) => new WebSocket(url),
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
    this.events?.connection(this.attempts ? "reconnecting" : "connecting");
    let socket: SocketLike;
    try {
      socket = this.createSocket(this.url);
    } catch {
      this.retry();
      return;
    }
    this.socket = socket;
    // A stalled handshake must not leave the composer waiting indefinitely.
    this.handshake = setTimeout(() => socket.close(), 15_000);
    socket.onopen = () => this.write(frame("hello", { cursor: this.cursor() }));
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
        if (value.type === "ready") this.write(frame("msg.status", {}));
        // Event acknowledgements are separate from the durable replay cursor.
        if (value.seq !== "0" && value.type !== "error")
          this.write(frame("msg.ack", { seq: value.seq }));
      } catch (error) {
        this.events?.error(error instanceof Error ? error.message : "Invalid chat frame.");
        // Stop on a contract mismatch; retrying the same incompatible server cannot fix it.
        this.stop();
        this.events?.connection("offline");
      }
    };
    socket.onerror = () => socket.close();
    socket.onclose = () => {
      clearTimeout(this.handshake);
      this.ready = false;
      this.retry();
    };
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
    this.socket.send(JSON.stringify(value));
  }

  send(text: string, clientID: string) {
    if (!this.ready) throw new Error("Wait for chat to reconnect. Your draft is still here.");
    // Never replay a send after reconnect: client_msg_id is correlation, not idempotency.
    this.write(frame("msg.send", { client_msg_id: clientID, text }, clientID));
  }
  abort(turn: string) {
    this.write(frame("msg.abort", { turn }));
  }
  sync(cursor: string) {
    this.write(frame("sync", { cursor }));
  }
  presence(foreground: boolean) {
    if (this.ready) this.write(frame("presence", { fg: foreground }));
  }
}

import type { Transport, TransportEvents } from "../application/ports";
import type { ClientFrame } from "../domain/protocol";
import type { ContentCodec } from "./content-worker";

import { parseFrameValue } from "../domain/protocol";
import { ContentOperationError } from "./content-error";
import { ContentWorker } from "./content-worker";
import { clientFrame, frame } from "./envelope";
import { authenticatedFetch, requestProof, sessionSigner } from "./proof";

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
  private readonly outgoing = new Set<Promise<void>>();
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
    readonly content: ContentCodec = new ContentWorker(),
  ) {
    content.onFailure = (error) => {
      if (this.stopped) return;
      this.events?.error(error.message);
      this.socket?.close();
    };
    content.onRefresh = () => this.refreshContent();
  }

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
      void this.receive(event, socket, generation).catch((error: unknown) => {
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

  private write(data: string) {
    if (this.socket?.readyState !== 1)
      throw new Error("Chat is disconnected. Your draft is still here.");
    this.socket.send(data);
  }

  send(text: string, clientID: string) {
    if (!this.ready) throw new Error("Wait for chat to reconnect. Your draft is still here.");
    // Never replay a send after reconnect: client_msg_id is correlation, not idempotency.
    const socket = this.socket;
    const generation = this.generation;
    const sending = this.sendSealed(text, clientID, socket, generation).catch((error: unknown) => {
      if (!this.stopped && generation === this.generation)
        this.events?.frame(
          parseFrameValue(
            frame("error", {
              code: error instanceof ContentOperationError ? error.code : "seal_failed",
              ref: clientID,
            }),
          ),
        );
    });
    this.outgoing.add(sending);
    void sending.finally(() => {
      this.outgoing.delete(sending);
    });
  }

  private async sendSealed(
    text: string,
    clientID: string,
    socket: SocketLike | undefined,
    generation: number,
  ) {
    const sealed = await this.content.send(
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
    void Promise.all(this.outgoing).then(() =>
      !this.stopped && generation === this.generation && socket === this.socket
        ? socket?.close()
        : undefined,
    );
  }

  private async offer(socket: SocketLike, generation: number) {
    try {
      const binding = (await sessionSigner()).binding;
      const current = () =>
        !this.stopped && generation === this.generation && socket === this.socket;
      if (!current()) return;
      const hello = await this.content.offer(this.cursor(), binding);
      if (current()) this.write(hello);
    } catch {
      if (socket === this.socket && generation === this.generation && !this.stopped) {
        this.events?.error("Chat encryption could not start. Reconnecting to try again.");
        socket.close();
      }
    }
  }

  private async receive(event: MessageEvent, socket: SocketLike, generation: number) {
    const current = () => generation === this.generation && socket === this.socket && !this.stopped;
    if (!current()) return;
    if (typeof event.data !== "string") throw new Error("Expected a text chat frame.");
    const result = await this.content.receive(event.data);
    if (!current()) return;
    if (result.kind === "key") return;
    if (result.kind === "error") {
      if (result.code === "invalid_frame") throw new Error(result.message);
      this.events?.error(result.message);
      return;
    }
    if (result.kind !== "frame") throw new Error("Unexpected content worker result.");
    const value = result.frame;
    if (value.type === "ready" && !this.refreshing) {
      this.ready = true;
      this.attempts = 0;
      clearTimeout(this.handshake);
      this.events?.connection("connected");
    }
    this.events?.frame(value);
    if (value.type === "ready") this.control(clientFrame("msg.status", {}));
    if (value.seq !== "0" && value.type !== "error")
      this.control(clientFrame("msg.ack", { seq: value.seq }));
    this.refreshContent();
  }

  private control(value: ClientFrame) {
    if (this.socket?.readyState !== 1)
      throw new Error("Chat is disconnected. Your draft is still here.");
    const socket = this.socket;
    const generation = this.generation;
    const send = async () => {
      const data = await this.content.send(value);
      if (!this.stopped && generation === this.generation && socket === this.socket)
        this.write(data);
    };
    const sending = send().catch(() => {
      if (!this.stopped && generation === this.generation) {
        this.events?.error("Chat control could not be sent. Reconnecting to recover.");
        socket.close();
      }
    });
    this.outgoing.add(sending);
    void sending.finally(() => {
      this.outgoing.delete(sending);
    });
  }

  abort(turn: string) {
    this.control(clientFrame("msg.abort", { turn }));
  }
  sync(cursor: string) {
    this.control(clientFrame("sync", { cursor }));
  }
  presence(foreground: boolean) {
    if (this.ready) this.control(clientFrame("presence", { fg: foreground }));
  }
}

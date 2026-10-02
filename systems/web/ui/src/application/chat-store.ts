import type { ChatState, Pending } from "../domain/chat";
import type { ServerFrame } from "../domain/protocol";
import type { Page } from "../generated/protocol";
import type { History, Transport } from "./ports";

import { reconcileHistory, reduceFrame } from "../domain/chat";

export class ChatStore {
  private state: ChatState = {
    connection: "connecting",
    messages: [],
    pending: [],
    active: null,
    cursor: "0",
    hasMore: false,
    loadingHistory: false,
    notice: null,
    error: null,
  };
  private listeners = new Set<() => void>();
  private seenEvents = new Set<string>();
  private controller?: AbortController;
  private generation = 0;
  private recovering = false;
  private historyBefore = "0";
  private refreshPending = false;
  private awaitingHistory = "0";
  private historyRetries = 0;
  private historyRetry?: ReturnType<typeof setTimeout>;

  constructor(
    readonly transport: Transport,
    private readonly history: History,
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };
  private update(patch: Partial<ChatState>) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }

  start() {
    const generation = ++this.generation;
    this.controller = new AbortController();
    this.update({ loadingHistory: false });
    this.transport.start(
      {
        frame: (value) => this.consume(value),
        error: (error) => this.update({ error }),
        connection: (connection) => {
          this.seenEvents.clear();
          const pending =
            connection === "reconnecting"
              ? this.state.pending.map((p): Pending =>
                  p.state === "sending" ? { ...p, state: "uncertain" } : p,
                )
              : this.state.pending;
          this.update({ connection, pending });
        },
      },
      () => this.state.cursor,
    );
    void this.loadHistory(false, generation);
  }
  stop() {
    ++this.generation;
    clearTimeout(this.historyRetry);
    this.controller?.abort();
    this.transport.stop();
  }
  dismiss() {
    this.update({ error: null, notice: null });
  }
  presence(foreground: boolean) {
    this.transport.presence(foreground);
  }

  send(input: string) {
    const text = input.trim();
    if (text === "") return false;
    if (new TextEncoder().encode(text).length > 64 * 1024) {
      this.update({ error: "This message is too long. Keep it under 64 KiB." });
      return false;
    }
    const id = crypto.randomUUID();
    try {
      this.transport.send(text, id);
    } catch (error) {
      this.fail(error);
      return false;
    }
    const afterTurn = this.state.messages.at(-1)?.turn ?? "0";
    this.update({
      pending: [...this.state.pending, { id, text, state: "sending", afterTurn }],
      error: null,
    });
    return true;
  }
  abort() {
    try {
      this.transport.abort(this.state.active ?? "0");
      this.update({ notice: "Stopping the current response…" });
    } catch (error) {
      this.fail(error);
    }
  }
  private fail(error: unknown) {
    this.update({
      error: error instanceof Error ? error.message : "Chat is unavailable. Try again.",
    });
  }

  consume(value: ServerFrame) {
    if (value.seq !== "0" && value.type !== "snapshot") {
      if (this.seenEvents.has(value.id)) return;
      this.seenEvents.add(value.id);
      if (this.seenEvents.size > 4096) {
        const oldest = this.seenEvents.values().next();
        if (oldest.done !== true) this.seenEvents.delete(oldest.value);
      }
    }
    this.update(reduceFrame(this.state, value));
    if (value.type === "error" && value.payload.code === "resync_from_head") {
      void this.recover();
    } else if (value.type === "msg.final" && !value.payload.message) {
      if (value.payload.status === "completed") {
        this.awaitingHistory = value.payload.msg.turn;
        this.historyRetries = 0;
      }
      void this.loadHistory(false);
    }
  }

  async loadHistory(older = true, generation = this.generation) {
    if (this.state.loadingHistory) {
      if (!older) this.refreshPending = true;
      return;
    }
    this.update({ loadingHistory: true });
    try {
      const before = older ? this.historyBefore : "0";
      const page = await this.history(before, this.controller?.signal);
      if (generation !== this.generation) return;
      this.applyPage(page, older);
      if (!older && this.awaitingHistory !== "0") {
        clearTimeout(this.historyRetry);
        if (page.turns.some((turn) => turn.seq === this.awaitingHistory)) {
          this.awaitingHistory = "0";
        } else if (this.historyRetries++ < 8) {
          // The terminal stream event can precede the durable transcript write.
          this.historyRetry = setTimeout(() => {
            void this.loadHistory(false, generation);
          }, 1000);
        }
      }
    } catch (error) {
      if (generation === this.generation) this.fail(error);
    } finally {
      if (generation === this.generation) {
        this.update({ loadingHistory: false });
        if (this.refreshPending) {
          this.refreshPending = false;
          void this.loadHistory(false);
        }
      }
    }
  }

  private applyPage(page: Page, older: boolean) {
    const patch = reconcileHistory(this.state.messages, this.state.pending, page);
    if (older || this.historyBefore === "0") {
      this.historyBefore = page.turns.at(-1)?.seq ?? this.historyBefore;
      this.update({ hasMore: page.has_more });
    }
    this.update(patch);
    // Fetching the newest page alone cannot prove earlier replay was consumed.
    // ready advances the resume cursor; history is only used to reset it during resync.
  }

  private async recover() {
    if (this.recovering) return;
    this.recovering = true;
    const generation = this.generation;
    try {
      const page = await this.history("0", this.controller?.signal);
      if (generation !== this.generation) return;
      this.update({
        messages: [],
        active: null,
        notice: "Chat reconnected. Recent history refreshed.",
      });
      this.historyBefore = "0";
      this.applyPage(page, false);
      const cursor = page.turns[0]?.seq ?? "0";
      this.update({ cursor });
      this.transport.sync(cursor);
    } catch (error) {
      if (generation === this.generation) this.fail(error);
    } finally {
      this.recovering = false;
    }
  }

  async findMessage(turn: string, ordinal: number): Promise<string | null> {
    const key = `${turn}:${ordinal}`;
    while (!this.state.messages.some((m) => m.key === key) && this.state.hasMore) {
      const previous = this.historyBefore;
      await this.loadHistory();
      if (previous === this.historyBefore) break;
    }
    return this.state.messages.some((m) => m.key === key) ? key : null;
  }
}

import type { Message, Page, Part } from "./generated/protocol";
import { compareSeq, parsePage } from "./protocol";
import type { ServerFrame } from "./protocol";
import type { Connection, Transport } from "./transport";

export interface ChatMessage extends Message {
  key: string;
  turn: string;
  ts: string;
  status?: string;
  model?: string;
}
export interface Pending {
  id: string;
  text: string;
  state:
    | "sending"
    | "accepted"
    | "queued"
    | "running"
    | "finished"
    | "stopped"
    | "uncertain"
    | "failed";
  afterTurn: string;
  turn?: string;
}
export interface ChatState {
  connection: Connection;
  messages: ChatMessage[];
  pending: Pending[];
  active: string | null;
  cursor: string;
  hasMore: boolean;
  loadingHistory: boolean;
  notice: string | null;
  error: string | null;
}
type History = (before: string, signal?: AbortSignal) => Promise<Page>;

export async function fetchHistory(before: string, signal?: AbortSignal): Promise<Page> {
  const response = await fetch(`/api/turns?after_seq=${before}&limit=50`, {
    cache: "no-store",
    credentials: "same-origin",
    signal,
  });
  if (!response.ok)
    throw new Error(
      response.status === 401
        ? "Sign in again to load your history."
        : "History could not be loaded. Try again.",
    );
  return parsePage(await response.json());
}

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
    private readonly history: History = fetchHistory,
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
              ? this.state.pending.map((p) =>
                  p.state === "sending" ? { ...p, state: "uncertain" as const } : p,
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

  send(text: string) {
    text = text.trim();
    if (!text) return false;
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
      if (this.seenEvents.size > 4096)
        this.seenEvents.delete(this.seenEvents.values().next().value!);
    }
    switch (value.type) {
      case "ready":
        this.update({ cursor: value.payload.cursor });
        break;
      case "turn.start": {
        // Local sends follow the agent's FIFO queue. Keep their live lifecycle
        // separate from persistence: an aborted run may have no history record.
        const next = this.state.pending.find(
          (p) =>
            !p.turn && (p.state === "sending" || p.state === "accepted" || p.state === "queued"),
        );
        this.update({
          active: value.payload.turn,
          notice: null,
          pending: this.state.pending.map((p) =>
            p === next ? { ...p, state: "running", turn: value.payload.turn } : p,
          ),
        });
        break;
      }
      case "delta":
      case "snapshot":
        this.progress(value);
        break;
      case "msg.status": {
        const p = value.payload;
        const local = this.state.pending.find((item) => item.id === p.client_msg_id);
        const pending = this.state.pending.map((item) =>
          item.id === p.client_msg_id
            ? {
                ...item,
                state: item.turn
                  ? item.state
                  : ((p.queued ? "queued" : "accepted") as Pending["state"]),
              }
            : item,
        );
        this.update({
          pending,
          ...(p.state === "running"
            ? { active: p.turn }
            : p.state === "idle"
              ? { active: null }
              : p.state === "accepted" && this.state.active === null && !local?.turn
                ? { active: "0" }
                : {}),
        });
        break;
      }
      case "msg.final": {
        const p = value.payload;
        const key = `${p.msg.turn}:${p.msg.ordinal}`;
        const previous = this.state.messages.find((m) => m.key === key);
        const canonical = p.message;
        const parts = canonical?.parts ?? [
          ...(previous?.parts ?? []).filter((part) => part.part_type !== "text"),
          { ordinal: previous?.parts.length ?? 0, part_type: "text", text: p.full_text },
        ];
        const message: ChatMessage = {
          ...(canonical ?? { ordinal: -1, role: "assistant", parts }),
          key,
          turn: p.msg.turn,
          ts: value.ts,
          status: p.status,
          model: p.model_ref,
        };
        this.mergeMessages([message]);
        if (!canonical) {
          const starting =
            this.state.active === "0"
              ? this.state.pending.find(
                  (item) => !item.turn && (item.state === "sending" || item.state === "accepted"),
                )
              : undefined;
          this.update({
            active:
              this.state.active === p.msg.turn || this.state.active === "0"
                ? null
                : this.state.active,
            pending: this.state.pending.map((item) =>
              item.turn === p.msg.turn || item === starting
                ? {
                    ...item,
                    turn: p.msg.turn,
                    state:
                      p.status === "aborted"
                        ? "stopped"
                        : p.status === "failed"
                          ? "failed"
                          : "finished",
                  }
                : item,
            ),
            notice: p.status === "aborted" ? "Response stopped." : null,
            error:
              p.status === "failed"
                ? "The response failed. You can send another message."
                : this.state.error,
          });
          if (p.status === "completed") {
            this.awaitingHistory = p.msg.turn;
            this.historyRetries = 0;
          }
          void this.loadHistory(false);
        }
        break;
      }
      case "notice":
        this.update({ notice: value.payload.text });
        break;
      case "error": {
        const p = value.payload;
        if (p.code === "resync_from_head") {
          void this.recover();
          break;
        }
        const messages: Record<string, string> = {
          bridge_unavailable: "The agent is unavailable. Try again shortly.",
          too_many_devices: "Too many chat windows are open. Close one and reconnect.",
          invalid_message: "The message could not be accepted. Check its length and try again.",
          protocol_mismatch: "q15 was updated. Refresh this page to reconnect.",
        };
        this.update({
          error: messages[p.code] ?? `Chat error: ${p.code}`,
          pending: this.state.pending.map((item) =>
            item.id === p.ref ? { ...item, state: "failed" } : item,
          ),
        });
        break;
      }
    }
  }

  private progress(value: Extract<ServerFrame, { type: "delta" | "snapshot" }>) {
    const p = value.payload;
    const key = `${p.msg.turn}:-1`;
    const previous = this.state.messages.find((m) => m.key === key);
    let parts: Part[] = [...(previous?.parts ?? [])];
    if (value.type === "snapshot" && p.kind === "model_start") parts = [];
    else if (p.kind === "text" || p.kind === "reasoning") {
      const index = parts.findLastIndex((part) => part.part_type === p.kind);
      const entry = {
        ordinal: index < 0 ? parts.length : parts[index]!.ordinal,
        part_type: p.kind,
        text:
          value.type === "snapshot"
            ? p.text
            : (index < 0 ? "" : (parts[index]!.text ?? "")) + p.text,
      };
      if (index < 0) parts.push(entry);
      else parts[index] = entry;
      if (p.reasoning !== undefined) {
        parts = parts.filter((part) => part.part_type !== "reasoning");
        if (p.reasoning) parts.unshift({ ordinal: -1, part_type: "reasoning", text: p.reasoning });
      }
    } else if (p.kind === "tool_call")
      parts.push({ ordinal: parts.length, part_type: "tool_call", tool_call: p.call });
    else if (p.kind === "tool_result")
      parts.push({
        ordinal: parts.length,
        part_type: "tool_result",
        tool_call_id: p.call?.id,
        content: p.text,
        is_error: p.is_error,
      });
    else if (p.kind !== "model_start")
      parts.push({ ordinal: parts.length, part_type: p.kind, text: p.text });
    this.mergeMessages([
      {
        ordinal: -1,
        role: "assistant",
        parts,
        key,
        turn: p.msg.turn,
        ts: value.ts,
        status: "streaming",
        model: p.model_ref ?? previous?.model,
      },
    ]);
    this.update({ active: p.msg.turn });
  }

  private mergeMessages(incoming: ChatMessage[]) {
    const messages = new Map(this.state.messages.map((m) => [m.key, m]));
    for (const m of incoming) {
      if (m.ordinal >= 0) messages.delete(`${m.turn}:-1`);
      messages.set(m.key, m);
    }
    this.update({
      messages: [...messages.values()].sort(
        (a, b) => compareSeq(a.turn, b.turn) || a.ordinal - b.ordinal,
      ),
    });
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
    const incoming = page.turns.flatMap((t) =>
      t.messages.map((m) => ({
        ...m,
        key: `${t.seq}:${m.ordinal}`,
        turn: t.seq,
        ts: t.created_at,
      })),
    );
    this.mergeMessages(incoming);
    // Correlate accepted sends with complete user messages in order, never by an allocated head.
    const users = incoming
      .filter((m) => m.role === "user")
      .map((m) => ({
        turn: m.turn,
        text: m.parts
          .filter((p) => p.part_type === "text")
          .map((p) => p.text ?? "")
          .join(""),
      }));
    const pending = this.state.pending.filter((p) => {
      const index = users.findIndex(
        (user) => user.text === p.text && compareSeq(user.turn, p.afterTurn) > 0,
      );
      if (index < 0 || p.state === "failed") return true;
      users.splice(index, 1);
      return false;
    });
    if (older || this.historyBefore === "0") {
      this.historyBefore = page.turns.at(-1)?.seq ?? this.historyBefore;
      this.update({ hasMore: page.has_more });
    }
    this.update({ pending });
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

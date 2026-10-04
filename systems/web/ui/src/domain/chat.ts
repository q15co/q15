import type { Message, Page, Part } from "../generated/protocol";
import type { ServerFrame } from "./protocol";

import { compareSeq } from "./protocol";

export type Connection = "connecting" | "connected" | "reconnecting" | "offline" | "unauthorized";
export interface ChatMessage extends Readonly<Omit<Message, "parts">> {
  readonly parts: readonly Readonly<Part>[];
  readonly key: string;
  readonly turn: string;
  readonly ts: string;
  readonly status?: string;
  readonly model?: string;
  readonly loopTurn?: number;
  readonly loopStart?: number;
}
interface PendingInput {
  id: string;
  text: string;
  afterTurn: string;
}
export type Pending = Readonly<
  PendingInput &
    (
      | { state: "sending" | "accepted" | "queued" | "uncertain"; turn?: never }
      | { state: "running" | "finished" | "stopped"; turn: string }
      | { state: "failed"; turn?: string }
    )
>;
export interface ChatState {
  readonly connection: Connection;
  readonly messages: readonly ChatMessage[];
  readonly pending: readonly Pending[];
  readonly active: string | null;
  readonly cursor: string;
  readonly hasMore: boolean;
  readonly loadingHistory: boolean;
  readonly notice: string | null;
  readonly error: string | null;
}

export function groupTurns(messages: readonly ChatMessage[], active: string | null) {
  const turns = new Map<string, ChatMessage[]>();
  for (const message of messages) {
    const group = turns.get(message.turn) ?? [];
    group.push(message);
    turns.set(message.turn, group);
  }
  // Keep a turn's identity from startup through its first streaming snapshot.
  if (active !== null && !turns.has(active)) turns.set(active, []);
  return turns;
}

export function reduceFrame(state: Readonly<ChatState>, value: ServerFrame): ChatState {
  let next = state;
  const update = (patch: Partial<ChatState>) => {
    next = { ...next, ...patch };
  };
  switch (value.type) {
    case "ready":
      update({ cursor: value.payload.cursor });
      break;
    case "turn.start": {
      // Local sends follow the agent's FIFO queue. Keep their live lifecycle
      // separate from persistence: an aborted run may have no history record.
      const firstPending = next.pending.find(
        (p) =>
          p.turn === undefined &&
          (p.state === "sending" || p.state === "accepted" || p.state === "queued"),
      );
      update({
        active: value.payload.turn,
        notice: null,
        pending: next.pending.map((p): Pending =>
          p === firstPending ? { ...p, state: "running", turn: value.payload.turn } : p,
        ),
      });
      break;
    }
    case "delta":
    case "snapshot":
      update({
        messages: mergeMessages(next.messages, [progressMessage(next.messages, value)]),
        active: value.payload.msg.turn,
      });
      break;
    case "msg.status": {
      const p = value.payload;
      const local = next.pending.find((item) => item.id === p.client_msg_id);
      const pending = next.pending.map((item): Pending => {
        if (item.id !== p.client_msg_id || item.turn !== undefined) return item;
        return {
          id: item.id,
          text: item.text,
          afterTurn: item.afterTurn,
          state: p.queued ? "queued" : "accepted",
        };
      });
      update({
        pending,
        ...(p.state === "running"
          ? { active: p.turn }
          : p.state === "idle"
            ? { active: null }
            : p.state === "accepted" && next.active === null && local?.turn === undefined
              ? { active: "0" }
              : {}),
      });
      break;
    }
    case "msg.final": {
      const p = value.payload;
      const key = `${p.msg.turn}:${p.msg.ordinal}`;
      const previous = next.messages.find((m) => m.key === key);
      const canonical = p.message;
      const parts = canonical?.parts ?? [
        ...(previous?.parts ?? []).filter(
          (part) => part.part_type !== "text" || part.disposition === "commentary",
        ),
        {
          ordinal: previous?.parts.length ?? 0,
          part_type: "text",
          disposition: "final",
          text: p.full_text,
        },
      ];
      const message: ChatMessage = {
        ...(canonical ?? { ordinal: -1, role: "assistant", parts }),
        key,
        turn: p.msg.turn,
        ts: value.ts,
        status: p.status,
        ...(p.model_ref === undefined ? {} : { model: p.model_ref }),
      };
      update({ messages: mergeMessages(next.messages, [message]) });
      if (!canonical) {
        const starting =
          next.active === "0"
            ? next.pending.find(
                (item) =>
                  item.turn === undefined &&
                  (item.state === "sending" || item.state === "accepted"),
              )
            : undefined;
        update({
          active: next.active === p.msg.turn || next.active === "0" ? null : next.active,
          pending: next.pending.map((item): Pending =>
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
              : next.error,
        });
      }
      break;
    }
    case "notice":
      update({ notice: value.payload.text });
      break;
    case "error": {
      const p = value.payload;
      if (p.code === "resync_from_head") {
        break;
      }
      const messages: Record<string, string> = {
        seal_failed: "The message could not be encrypted and was not sent. Try again.",
        unseal_failed: "The agent could not decrypt this message. Reconnect and try again.",
        bridge_unavailable: "The agent is unavailable. Try again shortly.",
        too_many_devices: "Too many chat windows are open. Close one and reconnect.",
        invalid_message: "The message could not be accepted. Check its length and try again.",
        protocol_mismatch: "q15 was updated. Refresh this page to reconnect.",
      };
      update({
        error: messages[p.code] ?? `Chat error: ${p.code}`,
        pending: next.pending.map((item): Pending =>
          item.id === p.ref ? { ...item, state: "failed" } : item,
        ),
      });
      break;
    }
    case "pong":
      break;
    default:
      throw new Error(`Unhandled chat event: ${String(value satisfies never)}`);
  }
  return next;
}

function progressMessage(
  messages: readonly ChatMessage[],
  value: Extract<ServerFrame, { type: "delta" | "snapshot" }>,
): ChatMessage {
  const p = value.payload;
  const key = `${p.msg.turn}:-1`;
  const previous = messages.find((m) => m.key === key);
  let parts: Part[] = [...(previous?.parts ?? [])];
  let loopStart = previous?.loopStart ?? 0;
  let loopTurn = previous?.loopTurn;
  if (value.type === "snapshot" && p.kind === "model_start") {
    // Go omits loop_turn at zero, the engine's initial loop counter.
    const nextLoop = p.loop_turn ?? 0;
    if (loopTurn !== undefined && nextLoop > loopTurn) {
      // A new model loop follows completed tool work. Keep that activity;
      // only a retry of the same loop replaces the current model attempt.
      parts = parts.map((part) =>
        part.part_type === "text" ? { ...part, disposition: "commentary" } : part,
      );
      loopStart = parts.length;
    } else if (loopTurn === undefined || nextLoop < loopTurn) {
      // Reconnect can replay an earlier loop from the retained run log.
      // Rebuild from that boundary instead of appending duplicate tool work.
      parts = [];
      loopStart = 0;
    } else parts = parts.slice(0, loopStart);
    loopTurn = nextLoop;
  } else if (p.kind === "text" || p.kind === "reasoning") {
    const index = parts.findLastIndex((part, i) => i >= loopStart && part.part_type === p.kind);
    const previousPart = parts[index];
    const entry = {
      ordinal: previousPart?.ordinal ?? parts.length,
      part_type: p.kind,
      text: value.type === "snapshot" ? p.text : (previousPart?.text ?? "") + p.text,
    };
    if (index < 0) parts.push(entry);
    else parts[index] = entry;
    if (p.reasoning !== undefined) {
      const completed = parts.slice(0, loopStart);
      const current = parts.slice(loopStart).filter((part) => part.part_type !== "reasoning");
      parts = [
        ...completed,
        ...(p.reasoning === "" ? [] : [{ ordinal: -1, part_type: "reasoning", text: p.reasoning }]),
        ...current,
      ];
    }
  } else if (p.kind === "tool_call")
    parts.push({
      ordinal: parts.length,
      part_type: "tool_call",
      ...(p.call === undefined ? {} : { tool_call: p.call }),
    });
  else if (p.kind === "tool_result")
    parts.push({
      ordinal: parts.length,
      part_type: "tool_result",
      ...(p.call === undefined ? {} : { tool_call_id: p.call.id }),
      content: p.text,
      ...(p.is_error === undefined ? {} : { is_error: p.is_error }),
    });
  else if (p.kind !== "model_start")
    parts.push({ ordinal: parts.length, part_type: p.kind, text: p.text });
  const model = p.model_ref ?? previous?.model;
  return {
    ordinal: -1,
    role: "assistant",
    parts: parts.map((part, ordinal) => ({ ...part, ordinal })),
    key,
    turn: p.msg.turn,
    ts: value.ts,
    status: "streaming",
    ...(model === undefined ? {} : { model }),
    loopStart,
    ...(loopTurn === undefined ? {} : { loopTurn }),
  };
}

export function mergeMessages(
  current: readonly ChatMessage[],
  incoming: readonly ChatMessage[],
): ChatMessage[] {
  const messages = new Map(current.map((m) => [m.key, m]));
  for (const m of incoming) {
    if (m.ordinal >= 0) messages.delete(`${m.turn}:-1`);
    messages.set(m.key, m);
  }
  return [...messages.values()].toSorted((a, b) => {
    const turn = compareSeq(a.turn, b.turn);
    return turn === 0 ? a.ordinal - b.ordinal : turn;
  });
}

export function reconcileHistory(
  messages: readonly ChatMessage[],
  pendingInputs: readonly Pending[],
  page: Page,
) {
  const incoming = page.turns.flatMap((t) =>
    t.messages.map((m) => ({
      ...m,
      key: `${t.seq}:${m.ordinal}`,
      turn: t.seq,
      ts: t.created_at,
    })),
  );

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
  const pending = pendingInputs.filter((p) => {
    const index = users.findIndex(
      (user) => user.text === p.text && compareSeq(user.turn, p.afterTurn) > 0,
    );
    if (index < 0 || p.state === "failed") return true;
    users.splice(index, 1);
    return false;
  });

  return { messages: mergeMessages(messages, incoming), pending };
}

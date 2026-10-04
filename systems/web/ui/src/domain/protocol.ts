import type {
  AbortRequest,
  AckRequest,
  Chunk,
  Cursor,
  HelloPayload,
  KeyPayload,
  ErrorPayload,
  FinalPayload,
  Frame,
  Message,
  MessageID,
  NoticePayload,
  Page,
  Part,
  PresenceRequest,
  ProgressPayload,
  ReadyPayload,
  SendRequest,
  Sealed,
  StatusPayload,
  ToolCall,
  Turn,
  TurnStartPayload,
} from "../generated/protocol";

import { VERSION } from "../generated/protocol";
import { isRecord } from "../shared/type-guards";

export type Envelope<T extends string, P> = Omit<Frame, "type" | "payload"> & {
  type: T;
  payload: P;
};
export type ClientPayloads = {
  hello: HelloPayload;
  sync: Cursor;
  "msg.send": SendRequest;
  "msg.abort": AbortRequest;
  "msg.ack": AckRequest;
  "msg.status": Record<string, never>;
  presence: PresenceRequest;
  ping: Record<string, never>;
};
export type ClientFrame = {
  [T in keyof ClientPayloads]: Envelope<T, ClientPayloads[T]>;
}[keyof ClientPayloads];
export type ServerFrame =
  | Envelope<"ready", ReadyPayload>
  | Envelope<"turn.start", TurnStartPayload>
  | Envelope<"delta" | "snapshot", ProgressPayload>
  | Envelope<"msg.final", FinalPayload>
  | Envelope<"msg.status", StatusPayload>
  | Envelope<"notice", NoticePayload>
  | Envelope<"error", ErrorPayload>
  | Envelope<"pong", Record<string, never>>;

export const decimal = (value: unknown): value is string =>
  typeof value === "string" &&
  /^(0|[1-9]\d{0,18})$/u.test(value) &&
  BigInt(value) <= 9223372036854775807n;
const integer = (value: unknown): value is number => Number.isInteger(value);
const optionalString = (value: unknown) => value === undefined || typeof value === "string";
const optionalBool = (value: unknown) => value === undefined || typeof value === "boolean";
const call = (value: unknown): value is ToolCall =>
  isRecord(value) &&
  typeof value.id === "string" &&
  typeof value.name === "string" &&
  typeof value.arguments === "string";
const identity = (value: unknown): value is MessageID =>
  isRecord(value) && decimal(value.turn) && integer(value.ordinal);
function part(value: unknown): value is Part {
  return (
    isRecord(value) &&
    integer(value.ordinal) &&
    typeof value.part_type === "string" &&
    [
      value.text,
      value.content,
      value.tool_call_id,
      value.disposition,
      value.media_kind,
      value.media_ref,
    ].every((field) => optionalString(field)) &&
    optionalBool(value.is_error) &&
    (value.tool_call === undefined || call(value.tool_call))
  );
}
function message(value: unknown): value is Message {
  return (
    isRecord(value) &&
    integer(value.ordinal) &&
    typeof value.role === "string" &&
    Array.isArray(value.parts) &&
    value.parts.every(part)
  );
}

function isEnvelope(value: unknown): value is Frame {
  return (
    isRecord(value) &&
    value.v === VERSION &&
    typeof value.id === "string" &&
    typeof value.type === "string" &&
    typeof value.ts === "string" &&
    decimal(value.seq) &&
    isRecord(value.payload)
  );
}

function isServerFrame(f: Frame): f is ServerFrame {
  const p = f.payload;
  if (!isRecord(p)) return false;
  switch (f.type) {
    case "ready":
      return decimal(p.cursor) && decimal(p.head_seq) && typeof p.device_id === "string";
    case "turn.start":
      return decimal(p.turn) && identity(p.msg);
    case "delta":
    case "snapshot":
      return (
        identity(p.msg) &&
        decimal(p.seq) &&
        typeof p.kind === "string" &&
        typeof p.text === "string" &&
        optionalString(p.reasoning) &&
        optionalString(p.model_ref) &&
        (p.loop_turn === undefined || integer(p.loop_turn)) &&
        optionalBool(p.is_error) &&
        (p.call === undefined || call(p.call))
      );
    case "msg.final":
      return (
        identity(p.msg) &&
        typeof p.full_text === "string" &&
        typeof p.status === "string" &&
        optionalString(p.model_ref) &&
        (p.message === undefined || message(p.message))
      );
    case "msg.status":
      return (
        decimal(p.turn) &&
        typeof p.state === "string" &&
        typeof p.queued === "boolean" &&
        optionalString(p.client_msg_id)
      );
    case "notice":
      return typeof p.code === "string" && typeof p.text === "string";
    case "error":
      return (
        typeof p.code === "string" &&
        typeof p.ref === "string" &&
        (p.head_seq === undefined || decimal(p.head_seq))
      );
    case "pong":
      return Object.keys(p).length === 0;
    default:
      return false;
  }
}

function isClientFrame(f: Frame): f is ClientFrame {
  const p = f.payload;
  if (!isRecord(p)) return false;
  switch (f.type) {
    case "hello":
      return decimal(p.cursor) && typeof p.public_key === "string" && typeof p.binding === "string";
    case "sync":
      return decimal(p.cursor);
    case "msg.send":
      return typeof p.client_msg_id === "string" && typeof p.text === "string";
    case "msg.abort":
      return decimal(p.turn);
    case "msg.ack":
      return decimal(p.seq);
    case "presence":
      return typeof p.fg === "boolean";
    case "msg.status":
    case "ping":
      return Object.keys(p).length === 0;
    default:
      return false;
  }
}

export function parseClientFrame(data: string): ClientFrame {
  const value: unknown = JSON.parse(data);
  if (!isEnvelope(value) || !isClientFrame(value)) {
    throw new Error("Unsupported client chat frame.");
  }
  return value;
}

export function parseFrame(data: string): ServerFrame {
  const f: unknown = JSON.parse(data);
  if (!isEnvelope(f)) {
    throw new Error("The server sent an unsupported chat frame. Refresh after updating q15.");
  }
  if (!isServerFrame(f)) {
    throw new Error(`Unsupported chat event: ${f.type}. Refresh after updating q15.`);
  }
  return f;
}

function isTurn(value: unknown): value is Turn {
  return (
    isRecord(value) &&
    decimal(value.seq) &&
    typeof value.created_at === "string" &&
    Array.isArray(value.messages) &&
    value.messages.every(message)
  );
}

function isPage(value: unknown): value is Page {
  return (
    isRecord(value) &&
    decimal(value.head_seq) &&
    typeof value.has_more === "boolean" &&
    Array.isArray(value.turns) &&
    value.turns.every(isTurn)
  );
}

export function parsePage(value: unknown): Page {
  if (!isPage(value)) throw new Error("The server returned unsupported history.");
  return value;
}

export const compareSeq = (a: string, b: string) =>
  BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0;

export type WireFrame = Frame | Envelope<"key", KeyPayload>;
export function parseWireFrame(data: string): WireFrame {
  const value: unknown = JSON.parse(data);
  if (!isEnvelope(value))
    throw new Error("The server sent an unsupported chat frame. Refresh after updating q15.");
  if (value.type === "key") {
    const p = value.payload;
    if (
      !isRecord(p) ||
      typeof p.public_key !== "string" ||
      typeof p.binding !== "string" ||
      typeof p.channel_id !== "string"
    )
      throw new Error("Invalid content key.");
    return {
      ...value,
      type: "key",
      payload: { public_key: p.public_key, binding: p.binding, channel_id: p.channel_id },
    };
  }
  return value;
}

function sealedChunk(value: unknown): value is Chunk {
  return (
    isRecord(value) &&
    typeof value.index === "number" &&
    Number.isInteger(value.index) &&
    typeof value.final === "boolean" &&
    typeof value.data === "string" &&
    value.data.length <= 43712
  );
}
export function parseSealed(value: unknown): Sealed {
  if (
    !isRecord(value) ||
    value.version !== 1 ||
    typeof value.stream !== "string" ||
    !Array.isArray(value.chunks) ||
    value.chunks.length === 0 ||
    value.chunks.length > 514
  )
    throw new Error("Unsupported sealed envelope.");
  const chunks: unknown[] = value.chunks;
  if (!chunks.every((chunk) => sealedChunk(chunk))) throw new Error("Unsupported sealed envelope.");
  return { version: 1, stream: value.stream, chunks };
}

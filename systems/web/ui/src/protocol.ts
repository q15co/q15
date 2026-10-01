import { VERSION } from "./generated/protocol";
import type {
  ErrorPayload,
  FinalPayload,
  Frame,
  Message,
  NoticePayload,
  Page,
  Part,
  ProgressPayload,
  ReadyPayload,
  StatusPayload,
  TurnStartPayload,
} from "./generated/protocol";

type Envelope<T extends string, P> = Omit<Frame, "type" | "payload"> & { type: T; payload: P };
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
  /^(0|[1-9]\d{0,18})$/.test(value) &&
  BigInt(value) <= 9223372036854775807n;
const record = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);
const integer = (value: unknown): value is number => Number.isInteger(value);
const optionalString = (value: unknown) => value === undefined || typeof value === "string";
const optionalBool = (value: unknown) => value === undefined || typeof value === "boolean";
const call = (value: unknown) =>
  record(value) &&
  typeof value.id === "string" &&
  typeof value.name === "string" &&
  typeof value.arguments === "string";
const identity = (value: unknown) => record(value) && decimal(value.turn) && integer(value.ordinal);
function part(value: unknown): value is Part {
  return (
    record(value) &&
    integer(value.ordinal) &&
    typeof value.part_type === "string" &&
    [
      value.text,
      value.content,
      value.tool_call_id,
      value.disposition,
      value.media_kind,
      value.media_ref,
    ].every(optionalString) &&
    optionalBool(value.is_error) &&
    (value.tool_call === undefined || call(value.tool_call))
  );
}
function message(value: unknown): value is Message {
  return (
    record(value) &&
    integer(value.ordinal) &&
    typeof value.role === "string" &&
    Array.isArray(value.parts) &&
    value.parts.every(part)
  );
}

export function parseFrame(data: string): ServerFrame {
  const f: unknown = JSON.parse(data);
  if (
    !record(f) ||
    f.v !== VERSION ||
    typeof f.id !== "string" ||
    typeof f.type !== "string" ||
    typeof f.ts !== "string" ||
    !decimal(f.seq) ||
    !record(f.payload)
  ) {
    throw new Error("The server sent an unsupported chat frame. Refresh after updating q15.");
  }
  const p = f.payload;
  let valid = false;
  switch (f.type) {
    case "ready":
      valid = decimal(p.cursor) && decimal(p.head_seq) && typeof p.device_id === "string";
      break;
    case "turn.start":
      valid = decimal(p.turn) && identity(p.msg);
      break;
    case "delta":
    case "snapshot":
      valid =
        identity(p.msg) &&
        decimal(p.seq) &&
        typeof p.kind === "string" &&
        typeof p.text === "string" &&
        optionalString(p.reasoning) &&
        optionalString(p.model_ref) &&
        optionalBool(p.is_error) &&
        (p.call === undefined || call(p.call));
      break;
    case "msg.final":
      valid =
        identity(p.msg) &&
        typeof p.full_text === "string" &&
        typeof p.status === "string" &&
        optionalString(p.model_ref) &&
        (p.message === undefined || message(p.message));
      break;
    case "msg.status":
      valid =
        decimal(p.turn) &&
        typeof p.state === "string" &&
        typeof p.queued === "boolean" &&
        optionalString(p.client_msg_id);
      break;
    case "notice":
      valid = typeof p.code === "string" && typeof p.text === "string";
      break;
    case "error":
      valid =
        typeof p.code === "string" &&
        typeof p.ref === "string" &&
        (p.head_seq === undefined || decimal(p.head_seq));
      break;
    case "pong":
      valid = true;
      break;
  }
  if (!valid) throw new Error(`Unsupported chat event: ${f.type}. Refresh after updating q15.`);
  return f as ServerFrame;
}

export function parsePage(value: unknown): Page {
  if (
    !record(value) ||
    !decimal(value.head_seq) ||
    typeof value.has_more !== "boolean" ||
    !Array.isArray(value.turns) ||
    !value.turns.every(
      (t) =>
        record(t) &&
        decimal(t.seq) &&
        typeof t.created_at === "string" &&
        Array.isArray(t.messages) &&
        t.messages.every(message),
    )
  ) {
    throw new Error("The server returned unsupported history.");
  }
  return value as unknown as Page;
}

export function frame(type: string, payload: unknown, id: string = crypto.randomUUID()): Frame {
  return { v: VERSION, id, type, ts: new Date().toISOString(), seq: "0", payload };
}

export const compareSeq = (a: string, b: string) =>
  BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0;

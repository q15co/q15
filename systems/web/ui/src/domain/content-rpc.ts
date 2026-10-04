import type { Page } from "../generated/protocol";
import type { ClientFrame, ServerFrame } from "./protocol";

import { isRecord } from "../shared/type-guards";
import { decimal, parseClientValue, parseFrameValue, parsePage } from "./protocol";

export const MaxContentJobs = 64;
export const MaxQueuedContentBytes = 96 * 1024 * 1024;
export type ContentCommand =
  | { readonly op: "offer"; readonly cursor: string; readonly binding: string }
  | { readonly op: "receive"; readonly data: string }
  | { readonly op: "history"; readonly data: ArrayBuffer; readonly channel: string }
  | { readonly op: "send"; readonly frame: ClientFrame };
export type ContentRequest = ContentCommand & {
  readonly id: number;
  readonly generation: number;
};
export type ContentResult =
  | { readonly kind: "wire"; readonly data: string }
  | { readonly kind: "key"; readonly channel: string }
  | { readonly kind: "frame"; readonly frame: ServerFrame }
  | { readonly kind: "page"; readonly page: Page }
  | {
      readonly kind: "error";
      readonly code: "unseal_failed" | "message_too_large" | "content_too_large" | "invalid_frame";
      readonly message: string;
    };
export interface ContentReply {
  readonly id: number;
  readonly generation: number;
  readonly refresh: boolean;
  readonly elapsed: number;
  readonly result: ContentResult;
}
const identifier = (value: unknown): value is number =>
  typeof value === "number" && Number.isSafeInteger(value) && value >= 0;

export function contentBytes(command: ContentCommand): number {
  switch (command.op) {
    case "offer":
      return 512 + 3 * (command.cursor.length + command.binding.length);
    case "receive":
      return 3 * command.data.length;
    case "history":
      return command.data.byteLength;
    case "send":
      return (
        1024 +
        3 * command.frame.id.length +
        (command.frame.type === "msg.send"
          ? 3 * (command.frame.payload.text.length + command.frame.payload.client_msg_id.length)
          : 0)
      );
    default:
      throw new Error("Unknown content operation.");
  }
}

export function parseContentRequest(value: unknown): ContentRequest {
  if (!isRecord(value) || !identifier(value.id) || !identifier(value.generation))
    throw new Error("Invalid content worker request.");
  const { id, generation } = value;
  switch (value.op) {
    case "offer":
      if (decimal(value.cursor) && typeof value.binding === "string" && value.binding.length <= 128)
        return { id, generation, op: "offer", cursor: value.cursor, binding: value.binding };
      break;
    case "receive":
      if (typeof value.data === "string")
        return { id, generation, op: "receive", data: value.data };
      break;
    case "history":
      if (value.data instanceof ArrayBuffer && typeof value.channel === "string")
        return { id, generation, op: "history", data: value.data, channel: value.channel };
      break;
    case "send":
      return { id, generation, op: "send", frame: parseClientValue(value.frame) };
  }
  throw new Error("Invalid content worker operation.");
}

export function contentCancellation(value: unknown): { id: number; generation: number } | null {
  return isRecord(value) &&
    value.op === "cancel" &&
    identifier(value.id) &&
    identifier(value.generation)
    ? { id: value.id, generation: value.generation }
    : null;
}

function parseResult(value: unknown): ContentResult {
  if (isRecord(value)) {
    switch (value.kind) {
      case "wire":
        if (typeof value.data === "string") return { kind: "wire", data: value.data };
        break;
      case "key":
        if (
          typeof value.channel === "string" &&
          value.channel.length > 0 &&
          value.channel.length <= 128
        )
          return { kind: "key", channel: value.channel };
        break;
      case "frame":
        return { kind: "frame", frame: parseFrameValue(value.frame) };
      case "page":
        return { kind: "page", page: parsePage(value.page) };
      case "error":
        if (
          typeof value.message === "string" &&
          (value.code === "unseal_failed" ||
            value.code === "message_too_large" ||
            value.code === "content_too_large" ||
            value.code === "invalid_frame")
        )
          return { kind: "error", code: value.code, message: value.message };
    }
  }
  throw new Error("Invalid content worker result.");
}

export function parseContentReply(value: unknown): ContentReply {
  if (
    !isRecord(value) ||
    !identifier(value.id) ||
    !identifier(value.generation) ||
    typeof value.refresh !== "boolean" ||
    typeof value.elapsed !== "number" ||
    !Number.isFinite(value.elapsed) ||
    value.elapsed < 0
  )
    throw new Error("Invalid content worker reply.");
  return {
    id: value.id,
    generation: value.generation,
    refresh: value.refresh,
    elapsed: value.elapsed,
    result: parseResult(value.result),
  };
}

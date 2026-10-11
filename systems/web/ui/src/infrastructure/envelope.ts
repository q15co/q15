import type { ClientPayloads, Envelope } from "../domain/protocol";
import type { Frame } from "../generated/protocol.ts";

import { VERSION } from "../generated/protocol.ts";

export function frame(type: string, payload: unknown, id: string = crypto.randomUUID()): Frame {
  return { v: VERSION, id, type, ts: new Date().toISOString(), seq: "0", payload };
}

export function clientFrame<T extends keyof ClientPayloads>(
  type: T,
  payload: ClientPayloads[T],
  id: string = crypto.randomUUID(),
): Envelope<T, ClientPayloads[T]> {
  return { v: VERSION, id, type, ts: new Date().toISOString(), seq: "0", payload };
}

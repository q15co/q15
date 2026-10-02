import type { Connection } from "../domain/chat";
import type { ServerFrame } from "../domain/protocol";
import type { Page } from "../generated/protocol";

export type History = (before: string, signal?: AbortSignal) => Promise<Page>;
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

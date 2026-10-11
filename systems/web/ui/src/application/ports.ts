import type { Connection } from "../domain/chat";
import type { ServerFrame } from "../domain/protocol";
import type { Attachment, Page } from "../generated/protocol";

export type History = (before: string, signal?: AbortSignal) => Promise<Page>;
export interface TransportEvents {
  frame: (value: ServerFrame) => void;
  connection: (value: Connection) => void;
  error: (message: string) => void;
}
export interface Transport {
  start: (events: TransportEvents, cursor: () => string) => void;
  stop: () => void;
  send: (text: string, clientID: string, parts?: readonly Attachment[]) => void;
  abort: (turn: string) => void;
  sync: (cursor: string) => void;
  presence: (foreground: boolean) => void;
}

export interface MediaObject {
  readonly url: string;
  readonly filename: string;
  readonly contentType: string;
  readonly dispose: () => void;
}
export interface Media {
  readonly upload: (files: readonly File[]) => Promise<readonly Attachment[]>;
  readonly load: (ref: string, signal: AbortSignal) => Promise<MediaObject>;
}

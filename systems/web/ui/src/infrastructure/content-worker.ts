import source from "virtual:q15-content-worker";

import type { ContentCommand, ContentRequest, ContentResult } from "../domain/content-rpc";
import type { ClientFrame } from "../domain/protocol";
import type { Page } from "../generated/protocol";

import {
  contentBytes,
  MaxContentJobs,
  MaxQueuedContentBytes,
  parseContentReply,
} from "../domain/content-rpc";
import { ContentOperationError } from "./content-error";

export interface ContentWorkerPort {
  onmessage: ((event: MessageEvent<unknown>) => void) | null;
  onerror: ((event: ErrorEvent) => void) | null;
  onmessageerror: ((event: MessageEvent<unknown>) => void) | null;
  postMessage(value: unknown, transfer: Transferable[]): void;
  terminate(): void;
}
export interface ContentChannel {
  readonly id: string;
  readonly generation: number;
}
export interface ContentCodec {
  readonly needsRefresh: boolean;
  onFailure: (error: Error) => void;
  onRefresh: () => void;
  reset(): void;
  offer(cursor: string, binding: string): Promise<string>;
  receive(data: string): Promise<ContentResult>;
  send(frame: ClientFrame): Promise<string>;
  channel(signal?: AbortSignal): Promise<ContentChannel>;
  history(data: ArrayBuffer, channel: ContentChannel, signal?: AbortSignal): Promise<Page>;
}
const cancelled = () => new DOMException("Content operation cancelled.", "AbortError");

function createWorker(): ContentWorkerPort {
  const url = URL.createObjectURL(new Blob([source], { type: "text/javascript" }));
  try {
    return new Worker(url, { name: "q15-content" });
  } finally {
    URL.revokeObjectURL(url);
  }
}

interface Pending {
  readonly bytes: number;
  readonly op: ContentCommand["op"];
  readonly started: number;
  readonly resolve: (value: ContentResult) => void;
  readonly reject: (error: Error) => void;
  readonly cleanup: () => void;
}

export class ContentWorker implements ContentCodec {
  private worker: ContentWorkerPort | undefined;
  private generation = 0;
  private nextID = 0;
  private bytes = 0;
  private readonly pending = new Map<number, Pending>();
  private currentChannel = "";
  private refresh = false;
  private signalReady?: (ready: { ready: boolean; error?: Error }) => void;
  private ready = this.waiter();
  onFailure: ContentCodec["onFailure"] = () => {};
  onRefresh: ContentCodec["onRefresh"] = () => {};
  onTiming: (sample: { op: ContentCommand["op"]; roundTrip: number; worker: number }) => void =
    () => {};

  constructor(private readonly factory = createWorker) {}
  get needsRefresh() {
    return this.refresh;
  }
  private waiter() {
    return new Promise<{ ready: boolean; error?: Error }>((resolve) => {
      this.signalReady = resolve;
    });
  }
  reset(reason: Error = cancelled()) {
    this.generation++;
    const worker = this.worker;
    this.worker = undefined;
    if (worker) {
      worker.onmessage = worker.onerror = worker.onmessageerror = null;
      worker.terminate();
    }
    for (const operation of this.pending.values()) {
      operation.cleanup();
      operation.reject(reason);
    }
    this.pending.clear();
    this.bytes = 0;
    this.refresh = false;
    this.currentChannel = "";
    this.signalReady?.({ ready: false, error: reason });
    this.ready = this.waiter();
  }
  private fail(error: Error) {
    this.reset(error);
    this.onFailure(error);
  }
  private start() {
    const worker = this.factory();
    const generation = this.generation;
    this.worker = worker;
    worker.onmessage = (event) => {
      if (worker !== this.worker || generation !== this.generation) return;
      try {
        const reply = parseContentReply(event.data);
        if (reply.generation !== generation) return;
        const operation = this.pending.get(reply.id);
        // Cancellation can race a reply already posted by the worker.
        if (!operation && reply.id < this.nextID) return;
        if (!operation) throw new Error("Unexpected content worker reply.");
        if (
          reply.result.kind !== "error" &&
          !(
            operation.op === "receive" &&
            (reply.result.kind === "key" || reply.result.kind === "frame")
          ) &&
          !(operation.op === "history" && reply.result.kind === "page") &&
          !((operation.op === "offer" || operation.op === "send") && reply.result.kind === "wire")
        )
          throw new Error("Mismatched content worker reply.");
        this.pending.delete(reply.id);
        this.bytes -= operation.bytes;
        operation.cleanup();
        this.refresh = reply.refresh;
        if (reply.result.kind === "key") {
          this.currentChannel = reply.result.channel;
          this.signalReady?.({ ready: true });
        }
        operation.resolve(reply.result);
        // Socket callers enqueue acknowledgements/accepted sends before draining for refresh.
        if (this.refresh && operation.op === "history") this.onRefresh();
        this.onTiming({
          op: operation.op,
          roundTrip: performance.now() - operation.started,
          worker: reply.elapsed,
        });
      } catch {
        this.fail(new Error("Chat byte processing failed. Reconnect to try again."));
      }
    };
    const failure = (event: Event) => {
      event.preventDefault();
      if (worker === this.worker && generation === this.generation)
        this.fail(new Error("Chat worker stopped. Reconnect to try again."));
    };
    worker.onerror = worker.onmessageerror = failure;
    return worker;
  }
  private call(command: ContentCommand, signal?: AbortSignal): Promise<ContentResult> {
    if (signal?.aborted === true) return Promise.reject(cancelled());
    const bytes = contentBytes(command);
    if (this.pending.size >= MaxContentJobs || this.bytes + bytes > MaxQueuedContentBytes) {
      const error = new Error("Chat processing is overloaded. Reconnecting to recover.");
      this.fail(error);
      return Promise.reject(error);
    }
    let worker = this.worker;
    try {
      if (!worker) {
        if (command.op !== "offer") throw new Error("Chat worker is not connected.");
        worker = this.start();
      }
    } catch {
      const error = new Error("Chat worker could not start. Reconnect to try again.");
      this.fail(error);
      return Promise.reject(error);
    }
    const id = this.nextID++;
    const generation = this.generation;
    const request: ContentRequest = { ...command, id, generation };
    return new Promise((resolve, reject) => {
      const abort = () => {
        const pending = this.pending.get(id);
        if (!pending) return;
        this.pending.delete(id);
        this.bytes -= pending.bytes;
        pending.cleanup();
        reject(cancelled());
        try {
          worker.postMessage({ op: "cancel", id, generation }, []);
        } catch {
          this.fail(new Error("Chat worker stopped during cancellation."));
        }
      };
      // Includes startup and queued work: a failed worker cannot strand a handshake/history wait.
      const timer = setTimeout(() => {
        if (generation === this.generation)
          this.fail(new Error("Chat worker timed out. Reconnect to try again."));
      }, 15_000);
      this.pending.set(id, {
        bytes,
        op: command.op,
        started: performance.now(),
        resolve,
        reject,
        cleanup: () => {
          clearTimeout(timer);
          signal?.removeEventListener("abort", abort);
        },
      });
      this.bytes += bytes;
      signal?.addEventListener("abort", abort, { once: true });
      try {
        worker.postMessage(request, command.op === "history" ? [command.data] : []);
      } catch {
        this.fail(new Error("Chat worker could not receive the frame. Reconnect to try again."));
      }
    });
  }
  private wire(result: ContentResult): string {
    if (result.kind === "error") throw new ContentOperationError(result.code, result.message);
    if (result.kind !== "wire") throw new Error("Unexpected encoded content result.");
    return result.data;
  }
  async offer(cursor: string, binding: string) {
    return this.wire(await this.call({ op: "offer", cursor, binding }));
  }
  receive(data: string) {
    return this.call({ op: "receive", data });
  }
  async send(frame: ClientFrame) {
    return this.wire(await this.call({ op: "send", frame }));
  }
  async channel(signal?: AbortSignal): Promise<ContentChannel> {
    if (signal?.aborted === true) throw cancelled();
    const generation = this.generation;
    let abort: (() => void) | undefined;
    try {
      const ready = signal
        ? Promise.race([
            this.ready,
            new Promise<never>((_, reject) => {
              abort = () => reject(cancelled());
              signal.addEventListener("abort", abort, { once: true });
            }),
          ])
        : this.ready;
      const state = await ready;
      if (!state.ready) throw state.error ?? cancelled();
      if (generation !== this.generation || this.currentChannel === "") throw cancelled();
      return { id: this.currentChannel, generation };
    } finally {
      if (abort) signal?.removeEventListener("abort", abort);
    }
  }
  async history(data: ArrayBuffer, channel: ContentChannel, signal?: AbortSignal): Promise<Page> {
    if (channel.generation !== this.generation || channel.id !== this.currentChannel)
      throw cancelled();
    const result = await this.call({ op: "history", data, channel: channel.id }, signal);
    if (result.kind === "error") throw new ContentOperationError(result.code, result.message);
    if (result.kind !== "page") throw new Error("Unexpected history worker result.");
    return result.page;
  }
}

import type { Chunk, Frame, HelloPayload, Sealed } from "../generated/protocol";

import { parseSealed } from "../domain/protocol";
import {
  ChunkBytes,
  MaxEnvelopeBytes,
  MaxContentTypeBytes,
  MaxChunkDataChars,
  MaxContentStreams,
  RekeyAfter,
  PLAINTEXT_FRAME_TYPES,
} from "../generated/protocol";
import { isRecord } from "../shared/type-guards";
import { encode, sessionSigner } from "./proof";

const chunkSize = ChunkBytes;
const maxBytes = MaxEnvelopeBytes;
const jsonType = "application/json";
const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true });
const failure = () => new Error("This content could not be decrypted. Reconnect to try again.");

function decode(value: string): Uint8Array<ArrayBuffer> {
  if (!/^[\w-]*$/u.test(value)) throw failure();
  const bytes = Uint8Array.from(
    atob(value.replaceAll("-", "+").replaceAll("_", "/")),
    (c) => c.codePointAt(0) ?? 0,
  );
  if (encode(bytes) !== value) throw failure();
  return bytes;
}

function context(frame: Frame): string {
  return [frame.v, frame.id, frame.type, new Date(frame.ts).toISOString(), frame.seq].join("\n");
}

function associated(
  frame: Frame,
  stream: string,
  index: number,
  final: boolean,
): Uint8Array<ArrayBuffer> {
  return encoder.encode(["q15-content-chunk-v1", context(frame), stream, index, final].join("\n"));
}

function nonce(index: number): Uint8Array<ArrayBuffer> {
  const bytes = new Uint8Array(12);
  new DataView(bytes.buffer).setUint32(8, index);
  return bytes;
}

async function derive(
  key: CryptoKey,
  salt: ArrayBuffer,
  stream: string,
  direction: string,
  usage: "encrypt" | "decrypt",
): Promise<CryptoKey> {
  return crypto.subtle.deriveKey(
    {
      name: "HKDF",
      hash: "SHA-256",
      salt,
      info: encoder.encode(["q15-content-stream-v1", direction, stream].join("\n")),
    },
    key,
    { name: "AES-GCM", length: 256 },
    false,
    [usage],
  );
}

export function hasContent(type: string): boolean {
  return !PLAINTEXT_FRAME_TYPES.some((control) => control === type);
}

// Only opaque non-exportable CryptoKeys exist in memory. Reconnect establishes
// a fresh pair, and the agent wraps durable history for that pair on demand.
export class ContentSession {
  private privateKey: CryptoKey | undefined;
  private publicKey = "";
  private binding = "";
  private source: CryptoKey | undefined;
  private salt: ArrayBuffer | undefined;
  private channelID = "";
  private seen = new Set<string>();
  private sent = 0;
  private signalReady?: (value: boolean) => void;
  private ready = this.waiter();
  private generation = 0;

  get needsRefresh(): boolean {
    return this.seen.size >= RekeyAfter || this.sent >= RekeyAfter;
  }

  private waiter() {
    return new Promise<boolean>((resolve) => {
      this.signalReady = resolve;
    });
  }

  reset() {
    this.generation++;
    this.privateKey = this.source = undefined;
    this.salt = undefined;
    this.channelID = "";
    this.seen.clear();
    this.sent = 0;
    this.signalReady?.(false);
    this.ready = this.waiter();
  }

  async offer(cursor: string): Promise<HelloPayload> {
    const generation = this.generation;
    const signer = await sessionSigner();
    const pair = await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, false, [
      "deriveKey",
    ]);
    const publicKey = encode(new Uint8Array(await crypto.subtle.exportKey("raw", pair.publicKey)));
    if (generation !== this.generation) throw failure();
    this.privateKey = pair.privateKey;
    this.publicKey = publicKey;
    this.binding = signer.binding;
    return { cursor, public_key: publicKey, binding: signer.binding };
  }

  async accept(value: unknown) {
    const generation = this.generation;
    if (
      !isRecord(value) ||
      typeof value.public_key !== "string" ||
      typeof value.channel_id !== "string" ||
      typeof value.binding !== "string"
    )
      throw failure();
    if (
      !this.privateKey ||
      this.source ||
      value.binding !== this.binding ||
      value.channel_id.length === 0 ||
      value.channel_id.length > 128
    )
      throw failure();
    const peer = await crypto.subtle.importKey(
      "raw",
      decode(value.public_key),
      { name: "ECDH", namedCurve: "P-256" },
      false,
      [],
    );
    const source = await crypto.subtle.deriveKey(
      { name: "ECDH", public: peer },
      this.privateKey,
      "HKDF",
      false,
      ["deriveKey"],
    );
    const salt = await crypto.subtle.digest(
      "SHA-256",
      encoder.encode(["q15-content-v1", this.binding, this.publicKey, value.public_key].join("\n")),
    );
    if (generation !== this.generation) throw failure();
    this.source = source;
    this.salt = salt;
    this.privateKey = undefined;
    this.channelID = value.channel_id;
    this.signalReady?.(true);
  }

  async channel(signal?: AbortSignal): Promise<string> {
    if (signal?.aborted === true) throw new DOMException("History cancelled.", "AbortError");
    let abort: (() => void) | undefined;
    try {
      const ready = signal
        ? Promise.race([
            this.ready,
            new Promise<never>((_, reject) => {
              abort = () => reject(new DOMException("History cancelled.", "AbortError"));
              signal.addEventListener("abort", abort, { once: true });
            }),
          ])
        : this.ready;
      if (!(await ready) || this.channelID === "") throw failure();
      return this.channelID;
    } finally {
      if (abort) signal?.removeEventListener("abort", abort);
    }
  }

  async wrap(
    frame: Frame,
    contentType = jsonType,
    bytes = encoder.encode(JSON.stringify(frame.payload)),
  ): Promise<Frame> {
    const chunks: Sealed["chunks"] = [];
    const stream = await this.sealStream(frame, contentType, [bytes], (_, chunk) => {
      chunks.push(chunk);
      return Promise.resolve();
    });
    return { ...frame, payload: { version: 1, stream, chunks } };
  }

  async sealStream(
    frame: Frame,
    contentType: string,
    source: Iterable<Uint8Array> | AsyncIterable<Uint8Array>,
    emit: (stream: string, chunk: Chunk) => Promise<void>,
  ): Promise<string> {
    const generation = this.generation;
    const type = encoder.encode(contentType);
    if (
      !this.source ||
      !this.salt ||
      type.length === 0 ||
      type.length > MaxContentTypeBytes ||
      this.sent >= MaxContentStreams
    )
      throw failure();
    const stream = encode(crypto.getRandomValues(new Uint8Array(16)));
    const key = await derive(this.source, this.salt, stream, "browser-to-agent", "encrypt");
    const header = new Uint8Array(2 + type.length);
    new DataView(header.buffer).setUint16(0, type.length);
    header.set(type, 2);
    async function* input() {
      yield header;
      yield* source;
    }
    const buffer = new Uint8Array(chunkSize);
    let index = 0;
    let filled = 0;
    let total = 0;
    const send = async (final: boolean) => {
      const encrypted = await crypto.subtle.encrypt(
        {
          name: "AES-GCM",
          iv: nonce(index),
          additionalData: associated(frame, stream, index, final),
          tagLength: 128,
        },
        key,
        buffer.slice(0, filled),
      );
      await emit(stream, { index, final, data: encode(new Uint8Array(encrypted)) });
      index++;
      filled = 0;
    };
    for await (const bytes of input()) {
      total += bytes.length;
      if (total > maxBytes + header.length) throw failure();
      for (let offset = 0; offset < bytes.length;) {
        const count = Math.min(chunkSize - filled, bytes.length - offset);
        buffer.set(bytes.subarray(offset, offset + count), filled);
        offset += count;
        filled += count;
        if (filled === chunkSize) await send(false);
      }
    }
    await send(true);
    if (generation !== this.generation) throw failure();
    this.sent++;
    return stream;
  }

  async open(frame: Frame): Promise<Frame> {
    if (!this.source) throw failure();
    const value = parseSealed(frame.payload);
    const parts: Uint8Array<ArrayBuffer>[] = [];
    let total = 0;
    const kind = await this.openStream(frame, value.stream, value.chunks, (plain) => {
      parts.push(plain);
      total += plain.length;
      return Promise.resolve();
    });
    if (kind !== jsonType) throw failure();
    const bytes = new Uint8Array(total);
    let offset = 0;
    for (const part of parts) {
      bytes.set(part, offset);
      offset += part.length;
    }
    const payload: unknown = JSON.parse(decoder.decode(bytes));
    return { ...frame, payload };
  }
  // Consumers stage bytes until the final authenticated chunk succeeds.
  async openStream(
    frame: Frame,
    stream: string,
    chunks: Iterable<Chunk> | AsyncIterable<Chunk>,
    write: (bytes: Uint8Array<ArrayBuffer>) => Promise<void>,
  ): Promise<string> {
    const generation = this.generation;
    if (!this.source || !this.salt) throw failure();
    const value = { stream };
    const id = decode(stream);
    if (id.length !== 16 || this.seen.has(stream) || this.seen.size >= MaxContentStreams)
      throw failure();
    const key = await derive(this.source, this.salt, stream, "agent-to-browser", "decrypt");
    let total = 0;
    let index = 0;
    let finished = false;
    let contentType = "";
    for await (const chunk of chunks) {
      if (chunk.index !== index || finished || chunk.data.length > MaxChunkDataChars)
        throw failure();
      let plain = new Uint8Array(
        await crypto.subtle.decrypt(
          {
            name: "AES-GCM",
            iv: nonce(index),
            additionalData: associated(frame, value.stream, index, chunk.final),
            tagLength: 128,
          },
          key,
          decode(chunk.data),
        ),
      );
      if (!chunk.final && plain.length !== chunkSize) throw failure();
      if (index === 0) {
        if (plain.length < 2) throw failure();
        const length = new DataView(plain.buffer).getUint16(0);
        if (length === 0 || length > MaxContentTypeBytes || length + 2 > plain.length)
          throw failure();
        contentType = decoder.decode(plain.slice(2, 2 + length));
        plain = plain.slice(2 + length);
      }
      total += plain.length;
      if (total > maxBytes) throw failure();
      await write(plain);
      index++;
      finished = chunk.final;
    }
    if (!finished || generation !== this.generation) throw failure();
    this.seen.add(value.stream);
    return contentType;
  }
}

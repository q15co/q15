import {
  createCipheriv,
  createDecipheriv,
  createECDH,
  createHash,
  hkdfSync,
  randomBytes,
} from "node:crypto";

import type { Frame, HelloPayload, Sealed } from "../generated/protocol";

import { parseSealed } from "../domain/protocol.ts";
import { frame } from "../infrastructure/envelope.ts";

const context = (value: Frame) =>
  [value.v, value.id, value.type, new Date(value.ts).toISOString(), value.seq].join("\n");
const aad = (value: Frame, stream: string, index: number, final: boolean) =>
  Buffer.from(["q15-content-chunk-v1", context(value), stream, index, final].join("\n"));
const nonce = (index: number) => {
  const bytes = Buffer.alloc(12);
  bytes.writeUInt32BE(index, 8);
  return bytes;
};

// This peer lets the browser tests exercise real WebCrypto on the app side.
export class TestSealer {
  readonly channelID = randomBytes(16).toString("base64url");
  readonly key: Frame;
  private secret: Buffer;
  private salt: Buffer;

  constructor(hello: HelloPayload) {
    const peer = createECDH("prime256v1");
    const publicKey = peer.generateKeys().toString("base64url");
    this.secret = peer.computeSecret(Buffer.from(hello.public_key, "base64url"));
    this.salt = createHash("sha256")
      .update(["q15-content-v1", hello.binding, hello.public_key, publicKey].join("\n"))
      .digest();
    this.key = frame("key", {
      binding: hello.binding,
      public_key: publicKey,
      channel_id: this.channelID,
    });
  }

  seal(
    value: Frame,
    contentType = "application/json",
    payload = Buffer.from(JSON.stringify(value.payload)),
  ): Frame {
    if (
      !["delta", "snapshot", "msg.final", "notice", "history", "media.get", "media.done"].includes(
        value.type,
      )
    )
      return value;
    const stream = randomBytes(16).toString("base64url");
    const key = Buffer.from(
      hkdfSync(
        "sha256",
        this.secret,
        this.salt,
        ["q15-content-stream-v1", "agent-to-browser", stream].join("\n"),
        32,
      ),
    );
    const type = Buffer.from(contentType);
    const header = Buffer.alloc(2);
    header.writeUInt16BE(type.length);
    const source = Buffer.concat([header, type, payload]);
    const chunks: Sealed["chunks"] = [];
    for (let index = 0, offset = 0; offset <= source.length; index++, offset += 32768) {
      const bytes = source.subarray(offset, offset + 32768);
      const final = bytes.length < 32768;
      const box = createCipheriv("aes-256-gcm", key, nonce(index));
      box.setAAD(aad(value, stream, index, final));
      const data = Buffer.concat([box.update(bytes), box.final(), box.getAuthTag()]).toString(
        "base64url",
      );
      chunks.push({ index, final, data });
      if (final) break;
    }
    return { ...value, payload: { version: 1, stream, chunks } };
  }

  openBytes(value: Frame): { contentType: string; bytes: Buffer } {
    const envelope = parseSealed(value.payload);
    const key = Buffer.from(
      hkdfSync(
        "sha256",
        this.secret,
        this.salt,
        ["q15-content-stream-v1", "browser-to-agent", envelope.stream].join("\n"),
        32,
      ),
    );
    const chunks = envelope.chunks.map((chunk) => {
      const data = Buffer.from(chunk.data, "base64url");
      const box = createDecipheriv("aes-256-gcm", key, nonce(chunk.index));
      box.setAAD(aad(value, envelope.stream, chunk.index, chunk.final));
      box.setAuthTag(data.subarray(-16));
      return Buffer.concat([box.update(data.subarray(0, -16)), box.final()]);
    });
    const plain = Buffer.concat(chunks);
    const size = plain.readUInt16BE(0);
    return { contentType: plain.subarray(2, 2 + size).toString(), bytes: plain.subarray(2 + size) };
  }
  open(value: Frame): Frame {
    if (value.type !== "msg.send") return value;
    const payload: unknown = JSON.parse(this.openBytes(value).bytes.toString());
    return { ...value, payload };
  }
}

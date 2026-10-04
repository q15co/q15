import type { ContentCommand, ContentResult } from "../domain/content-rpc";

import { parseFrameValue, parsePage, parseWireFrame } from "../domain/protocol";
import { MaxClientFrameBytes, MaxMessageBytes, MaxServerFrameBytes } from "../generated/protocol";
import { clientFrame } from "./envelope";
import { ContentSession, hasContent } from "./seal";

const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true });
const tooLarge = (): ContentResult => ({
  kind: "error",
  code: "content_too_large",
  message: "This content is too large to load.",
});

export class ContentEngine {
  private readonly session = new ContentSession();
  private channel = "";
  get needsRefresh() {
    return this.session.needsRefresh;
  }
  async execute(command: ContentCommand): Promise<ContentResult> {
    switch (command.op) {
      case "offer": {
        const hello = await this.session.offer(command.cursor, command.binding);
        return this.encode(clientFrame("hello", hello));
      }
      case "send": {
        if (
          command.frame.type === "msg.send" &&
          encoder.encode(command.frame.payload.text).length > MaxMessageBytes
        )
          return {
            kind: "error",
            code: "message_too_large",
            message: "This message is too long. Keep it under 64 KiB.",
          };
        const frame = hasContent(command.frame.type)
          ? await this.session.wrap(command.frame)
          : command.frame;
        return this.encode(frame);
      }
      case "receive": {
        if (encoder.encode(command.data).length > MaxServerFrameBytes) return tooLarge();
        let wire = parseWireFrame(command.data);
        if (wire.type === "key") {
          await this.session.accept(wire.payload);
          this.channel = await this.session.channel();
          return { kind: "key", channel: this.channel };
        }
        if (hasContent(wire.type)) {
          try {
            wire = await this.session.open(wire);
          } catch {
            return {
              kind: "error",
              code: "unseal_failed",
              message: "This content could not be decrypted. Reconnect to try again.",
            };
          }
        }
        return { kind: "frame", frame: parseFrameValue(wire) };
      }
      case "history": {
        if (command.data.byteLength > MaxServerFrameBytes) return tooLarge();
        if (this.channel === "" || command.channel !== this.channel)
          throw new Error("History belongs to a retired content connection.");
        const wire = parseWireFrame(decoder.decode(command.data));
        if (wire.type !== "history" || wire.id !== this.channel)
          throw new Error("The server returned unsupported history.");
        return { kind: "page", page: parsePage((await this.session.open(wire)).payload) };
      }
      default:
        throw new Error("Unknown content operation.");
    }
  }
  private encode(frame: Parameters<ContentSession["wrap"]>[0]): ContentResult {
    const data = JSON.stringify(frame);
    return encoder.encode(data).length > MaxClientFrameBytes
      ? { kind: "error", code: "message_too_large", message: "This message is too large to send." }
      : { kind: "wire", data };
  }
}

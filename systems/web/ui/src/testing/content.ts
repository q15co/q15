import type { ClientFrame } from "../domain/protocol";
import type { ContentCodec, ContentChannel } from "../infrastructure/content-worker";

import { parseFrame, parsePage, parseWireFrame } from "../domain/protocol";
import { MaxClientFrameBytes } from "../generated/protocol";
import { ContentOperationError } from "../infrastructure/content-error";
import { clientFrame } from "../infrastructure/envelope";

export class PlainContent implements ContentCodec {
  onFailure: ContentCodec["onFailure"] = () => {};
  onRefresh = () => {};
  get needsRefresh() {
    return false;
  }
  reset() {}
  offer(cursor: string, binding: string) {
    return Promise.resolve(
      JSON.stringify(clientFrame("hello", { cursor, public_key: "test", binding })),
    );
  }
  send(value: ClientFrame) {
    const data = JSON.stringify(value);
    return new TextEncoder().encode(data).length > MaxClientFrameBytes
      ? Promise.reject(new ContentOperationError("message_too_large", "Too large"))
      : Promise.resolve(data);
  }
  receive(data: string) {
    const wire = parseWireFrame(data);
    return Promise.resolve(
      wire.type === "key"
        ? ({ kind: "key", channel: "channel" } satisfies Awaited<
            ReturnType<ContentCodec["receive"]>
          >)
        : ({ kind: "frame", frame: parseFrame(data) } satisfies Awaited<
            ReturnType<ContentCodec["receive"]>
          >),
    );
  }
  channel() {
    return Promise.resolve({ id: "channel", generation: 0 });
  }
  history(data: ArrayBuffer, channel: ContentChannel) {
    const wire = parseWireFrame(new TextDecoder().decode(data));
    if (wire.type !== "history" || wire.id !== channel.id) throw new Error("unsupported history");
    return Promise.resolve(parsePage(wire.payload));
  }
}

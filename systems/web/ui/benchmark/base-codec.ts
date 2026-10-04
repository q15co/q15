import type { ClientFrame } from "../src/domain/protocol";

import { parseFrame, parsePage, parseWireFrame } from "../src/domain/protocol";
import { MaxClientFrameBytes, MaxMessageBytes } from "../src/generated/protocol";
import { clientFrame } from "../src/infrastructure/envelope";
import { createSessionKey, saveSessionKey } from "../src/infrastructure/proof";
import { ContentSession, hasContent } from "../src/infrastructure/seal";

const content = new ContentSession();
let incoming: Promise<unknown> = Promise.resolve();
const signer = await createSessionKey();
await saveSessionKey(signer.key, "b".repeat(43));
export const workerTimings: { op: string; roundTrip: number; worker: number }[] = [];
export const codec = {
  async offer(cursor: string, _binding: string) {
    return JSON.stringify(clientFrame("hello", await content.offer(cursor, _binding)));
  },
  receive(data: string) {
    const received = incoming.then(() => open(data));
    incoming = received;
    return received;
  },
  async channel() {
    return { id: await content.channel(), generation: 0 };
  },
  async history(data: ArrayBuffer, channel: { id: string }) {
    const wire = parseWireFrame(new TextDecoder().decode(data));
    if (wire.type !== "history" || wire.id !== channel.id) throw new Error("Invalid history");
    return parsePage((await content.open(wire)).payload);
  },
  async send(frame: ClientFrame) {
    if (
      frame.type === "msg.send" &&
      new TextEncoder().encode(frame.payload.text).length > MaxMessageBytes
    )
      throw new Error("Large message");
    const data = JSON.stringify(hasContent(frame.type) ? await content.wrap(frame) : frame);
    if (new TextEncoder().encode(data).length > MaxClientFrameBytes) throw new Error("Large wire");
    return data;
  },
  reset() {
    content.reset();
  },
};

async function open(data: string) {
  let wire = parseWireFrame(data);
  if (wire.type === "key") {
    await content.accept(wire.payload);
    return { kind: "key", channel: await content.channel() };
  }
  if (hasContent(wire.type)) wire = await content.open(wire);
  return { kind: "frame", frame: parseFrame(JSON.stringify(wire)) };
}

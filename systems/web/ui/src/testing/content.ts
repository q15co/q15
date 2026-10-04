import type { Frame } from "../generated/protocol";

export class PlainContent {
  reset() {}
  offer(cursor: string) {
    return Promise.resolve({ cursor, public_key: "test", binding: "test" });
  }
  accept() {
    return Promise.resolve();
  }
  wrap(value: Frame) {
    return Promise.resolve(value);
  }
  open(value: Frame) {
    return Promise.resolve(value);
  }
  channel() {
    return Promise.resolve("channel");
  }
}
export const hasContent = (type: string) =>
  ["msg.send", "notice", "delta", "snapshot", "msg.final", "history"].includes(type);
